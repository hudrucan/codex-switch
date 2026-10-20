import Foundation
import Testing

@testable import CodexSwitchCore

@Suite @MainActor
struct RefreshPolicyTests {
  @MainActor final class Clock {
    var date = Date(timeIntervalSince1970: 1_792_483_200)
    func advance(_ seconds: TimeInterval) { date.addTimeInterval(seconds) }
  }

  @Test(arguments: [0.0, 300.0, 301.0])
  func openingMenuRefreshesOnlyWhenCacheIsOlderThanFiveMinutes(age: TimeInterval) async throws {
    let clock = Clock()
    let cached = freshStatusData(fetchedAt: clock.date.addingTimeInterval(-age))
    var steps: [ScriptedExecutor.Step] = [.init(command: .status, output: success(cached))]
    if age > 300 {
      steps += [
        .init(command: .refreshUsage, output: success(try refreshedAccounts())),
        .init(command: .status, output: success(freshStatusData(fetchedAt: clock.date))),
      ]
    }
    let (model, executor, _, _) = makeModel(steps, now: { clock.date })
    await model.menuOpened()
    #expect(await executor.commands == (age > 300 ? [.status, .refreshUsage, .status] : [.status]))
    model.menuClosed()
    clock.advance(86400)
    for _ in 0..<10 { await Task.yield() }
    #expect(await executor.remaining == 0)
    #expect(await executor.commands.count == (age > 300 ? 3 : 1))
  }

  @Test(arguments: ["missing", "invalid", "future", "empty"])
  func unavailableCacheRefreshesButEmptyAccountListDoesNot(state: String) async throws {
    let clock = Clock()
    var root = try #require(
      JSONSerialization.jsonObject(
        with: freshStatusData(fetchedAt: clock.date)) as? [String: Any])
    var accounts = try #require(root["accounts"] as? [[String: Any]])
    if state == "empty" {
      accounts = []
    } else {
      var usage = try #require(accounts[1]["usage"] as? [String: Any])
      if state == "missing" {
        accounts[1].removeValue(forKey: "usage")
      } else {
        usage["fetchedAt"] = state == "invalid" ? "invalid" : "2099-01-01T00:00:00Z"
        accounts[1]["usage"] = usage
      }
    }
    root["accounts"] = accounts
    let cached = try JSONSerialization.data(withJSONObject: root)
    var steps: [ScriptedExecutor.Step] = [.init(command: .status, output: success(cached))]
    if state != "empty" {
      steps += [
        .init(command: .refreshUsage, output: success(try refreshedAccounts())),
        .init(command: .status, output: success(freshStatusData(fetchedAt: clock.date))),
      ]
    }
    let (model, executor, _, _) = makeModel(steps, now: { clock.date })
    await model.menuOpened()
    #expect(await executor.commands.count == (state == "empty" ? 1 : 3))
    #expect(await executor.remaining == 0)
  }

  @Test func manualAndAutomaticRefreshShareCooldownMeasuredFromCompletion() async throws {
    let clock = Clock()
    let stale = freshStatusData(fetchedAt: clock.date.addingTimeInterval(-600))
    let gate = ExecutionGate()
    let (model, executor, _, _) = makeModel(
      [
        .init(command: .status, output: success(stale)),
        .init(command: .refreshUsage, output: success(try refreshedAccounts()), gate: gate),
        .init(command: .status, output: success(stale)),
        .init(command: .status, output: success(stale)),
        .init(command: .status, output: success(stale)),
        .init(command: .refreshUsage, output: success(try refreshedAccounts())),
        .init(command: .status, output: success(freshStatusData(fetchedAt: clock.date))),
      ], now: { clock.date })
    let opening = Task { await model.menuOpened() }
    await gate.waitForEntry()
    clock.advance(120)
    await model.refreshUsage()
    #expect(await executor.commands == [.status, .refreshUsage])
    await gate.resume()
    await opening.value
    await model.refreshUsage()
    clock.advance(59)
    await model.menuOpened()
    await model.refreshUsage()
    #expect(await executor.commands == [.status, .refreshUsage, .status, .status])
    #expect(model.refreshNotice?.hasPrefix("Refresh available after ") == true)
    clock.advance(1)
    await model.menuOpened()
    #expect(
      await executor.commands == [
        .status, .refreshUsage, .status, .status, .status, .refreshUsage, .status,
      ])
    #expect(model.refreshNotice == nil)
    #expect(await executor.remaining == 0)
  }

  @Test func failuresBackOffOneTwoFiveMinutesAndSuccessResetsTheSequence() async throws {
    let clock = Clock()
    var steps: [ScriptedExecutor.Step] = (0..<4).map { _ in
      .init(command: .refreshUsage, output: failedOutput())
    }
    steps += [
      .init(command: .refreshUsage, output: success(try refreshedAccounts())),
      .init(command: .status, output: success(freshStatusData(fetchedAt: clock.date))),
      .init(command: .refreshUsage, output: failedOutput()),
      .init(command: .refreshUsage, output: success(try refreshedAccounts())),
      .init(command: .status, output: success(freshStatusData(fetchedAt: clock.date))),
    ]
    let (model, executor, _, _) = makeModel(steps, now: { clock.date })
    for (index, delay) in [60.0, 120.0, 300.0, 300.0].enumerated() {
      await model.refreshUsage()
      #expect(model.errorMessage != nil)
      clock.advance(delay - 1)
      await model.refreshUsage()
      #expect(await executor.commands.count == index + 1)
      clock.advance(1)
    }
    await model.refreshUsage()
    #expect(model.errorMessage == nil)
    clock.advance(60)
    await model.refreshUsage()
    clock.advance(59)
    await model.refreshUsage()
    #expect(await executor.commands.count == 7)
    clock.advance(1)
    await model.refreshUsage()
    #expect(model.errorMessage == nil)
    #expect(await executor.remaining == 0)
  }

  @Test func closingMenuDuringStatusReadPreventsAutomaticRefresh() async {
    let clock = Clock()
    let gate = ExecutionGate()
    let (model, executor, _, _) = makeModel(
      [
        .init(command: .status, output: success(statusData()), gate: gate)
      ], now: { clock.date })
    let opening = Task { await model.menuOpened() }
    await gate.waitForEntry()
    model.menuClosed()
    await gate.resume()
    await opening.value
    clock.advance(3600)
    for _ in 0..<10 { await Task.yield() }
    #expect(await executor.commands == [.status])
    #expect(model.phase == .idle)
  }

  @Test func cancelledOpenDoesNotStartRefreshEvenIfItRunsAfterClose() async {
    let gate = ExecutionGate()
    let (model, executor, _, _) = makeModel([])
    let opening = Task {
      await gate.pause()
      await model.menuOpened()
    }
    await gate.waitForEntry()
    opening.cancel()
    model.menuClosed()
    await gate.resume()
    await opening.value
    #expect(await executor.commands.isEmpty)
  }

  @Test func closingMenuLetsStartedRefreshFinishWithoutSchedulingAnother() async throws {
    let clock = Clock()
    let gate = ExecutionGate()
    let cached = freshStatusData(fetchedAt: clock.date.addingTimeInterval(-600))
    let (model, executor, _, _) = makeModel(
      [
        .init(command: .status, output: success(cached)),
        .init(command: .refreshUsage, output: success(try refreshedAccounts()), gate: gate),
        .init(command: .status, output: success(cached)),
      ], now: { clock.date })
    let opening = Task { await model.menuOpened() }
    await gate.waitForEntry()
    model.menuClosed()
    opening.cancel()
    await gate.resume()
    await opening.value
    clock.advance(3600)
    for _ in 0..<10 { await Task.yield() }
    #expect(await executor.commands == [.status, .refreshUsage, .status])
    #expect(model.status == (try UIStatus.decode(cached)))
    #expect(model.phase == .idle)
  }

  @Test func passingResetTimeNeverInventsFreshQuotaOrTriggersRefresh() async throws {
    let clock = Clock()
    // Both synthetic reset timestamps are already past; the cache was just fetched.
    clock.advance(30 * 86400)
    let cached = freshStatusData(fetchedAt: clock.date)
    let (model, executor, _, _) = makeModel(
      [
        .init(command: .status, output: success(cached))
      ], now: { clock.date })
    await model.menuOpened()
    clock.advance(3600)
    for _ in 0..<10 { await Task.yield() }
    #expect(model.status?.accounts[0].usage?.rateLimits?.main.primary?.usedPercent == 38)
    #expect(await executor.commands == [.status])
  }

  @Test(arguments: [guiProcesses, terminalProcesses])
  func automaticRefreshNeverSwitchesOrTerminatesRunningProcesses(processes: String) async throws {
    let clock = Clock()
    let cached = freshStatusData(
      processes: processes, fetchedAt: clock.date.addingTimeInterval(-600))
    let (model, executor, applications, _) = makeModel(
      [
        .init(command: .status, output: success(cached)),
        .init(command: .refreshUsage, output: success(try refreshedAccounts())),
        .init(command: .status, output: success(cached)),
      ], now: { clock.date })
    await model.menuOpened()
    #expect(await executor.commands == [.status, .refreshUsage, .status])
    #expect(applications.terminations.isEmpty)
    #expect(model.pendingAlias == nil)
  }

  @Test(arguments: ["command", "account", "status"])
  func failedAutomaticRefreshPreservesCacheAndThrottlesManualRetry(failure: String) async throws {
    let clock = Clock()
    let cached = freshStatusData(fetchedAt: clock.date.addingTimeInterval(-600))
    var accounts = try #require(
      JSONSerialization.jsonObject(with: refreshedAccounts()) as? [[String: Any]])
    var usage = try #require(accounts[0]["usage"] as? [String: Any])
    usage["error"] = "synthetic refresh failure"
    accounts[0]["usage"] = usage
    let response =
      failure == "account"
      ? success(try JSONSerialization.data(withJSONObject: accounts))
      : success(try refreshedAccounts())
    var steps: [ScriptedExecutor.Step] = [
      .init(command: .status, output: success(cached)),
      .init(command: .refreshUsage, output: failure == "command" ? failedOutput() : response),
    ]
    if failure == "status" { steps.append(.init(command: .status, output: failedOutput())) }
    steps += [
      .init(command: .refreshUsage, output: success(try refreshedAccounts())),
      .init(command: .status, output: success(freshStatusData(fetchedAt: clock.date))),
    ]
    let (model, executor, _, _) = makeModel(steps, now: { clock.date })
    await model.menuOpened()
    #expect(model.status == (try UIStatus.decode(cached)))
    #expect(model.errorMessage != nil)
    clock.advance(59)
    await model.refreshUsage()
    #expect(await executor.remaining == 2)
    clock.advance(1)
    await model.refreshUsage()
    #expect(model.errorMessage == nil)
    #expect(await executor.remaining == 0)
  }
}
