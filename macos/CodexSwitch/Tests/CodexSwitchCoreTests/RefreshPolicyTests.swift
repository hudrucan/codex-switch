import Foundation
import Testing

@testable import CodexSwitchCore

@Suite @MainActor
struct RefreshPolicyTests {
  @MainActor final class Clock {
    var date = Date(timeIntervalSince1970: 1_792_483_200)
    func advance(_ seconds: TimeInterval) { date.addTimeInterval(seconds) }
  }

  @Test(arguments: [false, true])
  func completedAccountAppearsBeforeNextAccountFinishes(secondFails: Bool) async throws {
    let clock = Clock()
    let gate = ExecutionGate()
    let reloadGate = ExecutionGate()
    let cached = freshStatusData(fetchedAt: clock.date)
    let (model, executor, applications, _) = makeModel(
      [
        .init(command: .status, output: success(cached)),
        .init(command: .refreshUsage("work"), output: success(try refreshedAccount())),
        .init(
          command: .refreshUsage("personal"),
          output: secondFails
            ? failedOutput() : success(try refreshedAccount(alias: "personal", usedPercent: 10)),
          gate: gate),
        .init(command: .status, output: failedOutput(), gate: reloadGate),
      ], now: { clock.date })
    await model.menuOpened()
    let original = model.status
    let refresh = Task { await model.refreshUsage() }
    await gate.waitForEntry()
    #expect(model.phase == .refreshing)
    #expect(model.refreshCompleted == 1)
    #expect(model.refreshTotal == 2)
    #expect(model.refreshingAlias == "personal")
    #expect(model.refreshResults == ["a": true])
    #expect(model.status?.accounts[0].usage?.rateLimits?.main.primary?.usedPercent == 20)
    #expect(model.status?.accounts[1].usage == original?.accounts[1].usage)
    #expect(model.status?.observation == original?.observation)
    #expect(model.status?.codexProcesses == original?.codexProcesses)
    #expect(model.refreshStartedAt == clock.date)
    clock.advance(30)
    // A closed/cancelled menu must not interrupt the current CLI's recovery.
    model.menuClosed()
    refresh.cancel()
    await model.refreshUsage()
    await model.beginSwitch(accountID: "b")
    await model.openCodex()
    #expect(await executor.commands == [.status, .refreshUsage("work"), .refreshUsage("personal")])
    await gate.resume()
    await reloadGate.waitForEntry()
    #expect(model.isReloadingUsage)
    #expect(model.refreshCompleted == 2)
    #expect(model.progressLabel == "Loading updated status…")
    await reloadGate.resume()
    await refresh.value
    // Even failure of the final status read cannot discard completed updates.
    #expect(model.status?.accounts[0].usage?.rateLimits?.main.primary?.usedPercent == 20)
    #expect(
      model.status?.accounts[1].usage?.rateLimits?.main.primary?.usedPercent
        == (secondFails ? 38 : 10))
    #expect(model.refreshResults["b"] == !secondFails)
    #expect(model.errorMessage != nil)
    #expect(model.phase == .idle)
    #expect(model.refreshStartedAt == nil)
    #expect(!model.isReloadingUsage)
    #expect(applications.terminations.isEmpty)
    #expect(await executor.remaining == 0)
  }

  @Test func failedFirstAccountDoesNotBlockLaterAccount() async throws {
    let cached = freshStatusData()
    let gate = ExecutionGate()
    let (model, executor, _, _) = makeModel([
      .init(command: .status, output: success(cached)),
      .init(command: .refreshUsage("work"), output: failedOutput()),
      .init(
        command: .refreshUsage("personal"),
        output: success(try refreshedAccount(alias: "personal")), gate: gate),
      .init(command: .status, output: failedOutput()),
    ])
    await model.menuOpened()
    let refresh = Task { await model.refreshUsage() }
    await gate.waitForEntry()
    #expect(model.refreshResults["a"] == false)
    #expect(model.refreshingAlias == "personal")
    await gate.resume()
    await refresh.value
    #expect(model.status?.accounts[0].usage?.rateLimits?.main.primary?.usedPercent == 38)
    #expect(model.status?.accounts[1].usage?.rateLimits?.main.primary?.usedPercent == 20)
    #expect(model.refreshResults == ["a": false, "b": true])
    #expect(await executor.remaining == 0)
  }

  @Test func partialSuccessKeepsUpdatedUsageAndReloadsObservation() async throws {
    let cached = freshStatusData()
    var finalRoot = try #require(
      JSONSerialization.jsonObject(with: freshStatusData(active: "b")) as? [String: Any])
    var finalAccounts = try #require(finalRoot["accounts"] as? [[String: Any]])
    var updated = try #require(
      JSONSerialization.jsonObject(with: refreshedAccount()) as? [String: Any])
    updated["active"] = false
    finalAccounts[0] = updated
    finalRoot["accounts"] = finalAccounts
    let (model, executor, _, _) = makeModel([
      .init(command: .status, output: success(cached)),
      .init(command: .refreshUsage("work"), output: success(try refreshedAccount())),
      .init(command: .refreshUsage("personal"), output: failedOutput()),
      .init(
        command: .status, output: success(try JSONSerialization.data(withJSONObject: finalRoot))),
    ])
    await model.menuOpened()
    await model.refreshUsage()
    #expect(model.status?.activeAccountID == "b")
    #expect(model.status?.accounts[0].usage?.rateLimits?.main.primary?.usedPercent == 20)
    #expect(model.errorMessage == UIError.refreshFailed.errorDescription)
    #expect(await executor.remaining == 0)
  }

  @Test func ambiguousAliasIsSkippedWithoutBlockingOtherAccounts() async throws {
    var root = try #require(JSONSerialization.jsonObject(with: freshStatusData()) as? [String: Any])
    var accounts = try #require(root["accounts"] as? [[String: Any]])
    accounts[0]["alias"] = "b"
    root["accounts"] = accounts
    let cached = try JSONSerialization.data(withJSONObject: root)
    let (model, executor, _, _) = makeModel([
      .init(command: .status, output: success(cached)),
      .init(
        command: .refreshUsage("personal"), output: success(try refreshedAccount(alias: "personal"))
      ),
      .init(command: .status, output: failedOutput()),
    ])
    await model.menuOpened()
    await model.refreshUsage()
    #expect(model.refreshResults == ["a": false, "b": true])
    #expect(await executor.commands == [.status, .refreshUsage("personal"), .status])
    #expect(await executor.remaining == 0)
  }

  @Test(arguments: ["fresh", "stale", "missing", "invalid", "future", "empty"])
  func menuRefreshesOnlyWhenCacheRequiresIt(state: String) async throws {
    let clock = Clock()
    var root = try #require(
      JSONSerialization.jsonObject(with: freshStatusData(fetchedAt: clock.date)) as? [String: Any])
    var accounts = try #require(root["accounts"] as? [[String: Any]])
    if state == "empty" {
      accounts = []
    } else if state != "fresh" {
      var usage = try #require(accounts[1]["usage"] as? [String: Any])
      switch state {
      case "missing": accounts[1].removeValue(forKey: "usage")
      case "invalid": usage["fetchedAt"] = "invalid"
      case "future": usage["fetchedAt"] = "2099-01-01T00:00:00Z"
      default:
        usage["fetchedAt"] = ISO8601DateFormatter().string(
          from: clock.date.addingTimeInterval(-301))
      }
      if state != "missing" { accounts[1]["usage"] = usage }
    }
    root["accounts"] = accounts
    let cached = try JSONSerialization.data(withJSONObject: root)
    let needsRefresh = state != "fresh" && state != "empty"
    var steps: [ScriptedExecutor.Step] = [.init(command: .status, output: success(cached))]
    if needsRefresh {
      steps += [
        .init(command: .refreshUsage("work"), output: success(try refreshedAccount())),
        .init(
          command: .refreshUsage("personal"),
          output: success(try refreshedAccount(alias: "personal"))),
        .init(command: .status, output: success(cached)),
      ]
    }
    let (model, executor, _, _) = makeModel(steps, now: { clock.date })
    await model.menuOpened()
    #expect(await executor.commands.count == (needsRefresh ? 4 : 1))
    #expect(await executor.remaining == 0)
    model.menuClosed()
    clock.advance(86400)
    for _ in 0..<10 { await Task.yield() }
    #expect(await executor.commands.count == (needsRefresh ? 4 : 1))
  }

  @Test func cooldownStartsAfterWholeBatchAndFailuresBackOff() async throws {
    let clock = Clock()
    let gate = ExecutionGate()
    let cached = freshStatusData(fetchedAt: clock.date)
    var steps: [ScriptedExecutor.Step] = [.init(command: .status, output: success(cached))]
    for index in 0..<5 {
      let fails = index < 3
      steps += [
        .init(
          command: .refreshUsage("work"),
          output: fails ? failedOutput() : success(try refreshedAccount()),
          gate: index == 0 ? gate : nil),
        .init(
          command: .refreshUsage("personal"),
          output: success(try refreshedAccount(alias: "personal"))),
        .init(command: .status, output: success(cached)),
      ]
    }
    let (model, executor, _, _) = makeModel(steps, now: { clock.date })
    await model.menuOpened()
    let refresh = Task { await model.refreshUsage() }
    await gate.waitForEntry()
    clock.advance(120)
    await model.refreshUsage()
    #expect(await executor.commands.count == 2)
    await gate.resume()
    await refresh.value
    for (index, delay) in [60.0, 120.0, 300.0, 60.0].enumerated() {
      clock.advance(delay - 1)
      await model.refreshUsage()
      #expect(await executor.commands.count == 1 + (index + 1) * 3)
      #expect(model.refreshNotice?.hasPrefix("Refresh available after ") == true)
      clock.advance(1)
      await model.refreshUsage()
    }
    #expect(model.errorMessage == nil)
    #expect(model.refreshNotice == nil)
    #expect(await executor.remaining == 0)
  }

  @Test func closingMenuDuringStatusReadPreventsAutomaticRefresh() async {
    let gate = ExecutionGate()
    let (model, executor, _, _) = makeModel([
      .init(command: .status, output: success(statusData()), gate: gate)
    ])
    let opening = Task { await model.menuOpened() }
    await gate.waitForEntry()
    model.menuClosed()
    await gate.resume()
    await opening.value
    #expect(await executor.commands == [.status])
    #expect(model.phase == .idle)
  }

  @Test(arguments: [guiProcesses, terminalProcesses])
  func automaticRefreshNeverSwitchesOrTerminatesProcesses(processes: String) async throws {
    let clock = Clock()
    let cached = freshStatusData(
      processes: processes, fetchedAt: clock.date.addingTimeInterval(-600))
    let (model, executor, applications, _) = makeModel(
      [
        .init(command: .status, output: success(cached)),
        .init(command: .refreshUsage("work"), output: success(try refreshedAccount())),
        .init(
          command: .refreshUsage("personal"),
          output: success(try refreshedAccount(alias: "personal"))),
        .init(command: .status, output: success(cached)),
      ], now: { clock.date })
    await model.menuOpened()
    #expect(applications.terminations.isEmpty)
    #expect(model.pendingAlias == nil)
    #expect(await executor.commands.allSatisfy { !$0.arguments.contains("--allow-running") })
    #expect(await executor.remaining == 0)
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

}
