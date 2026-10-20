import Combine
import Foundation
import Testing

@testable import CodexSwitchCore

@Suite @MainActor
struct AppModelTests {
  @Test func eachForceTargetRequiresAnImmediateStatusRead() async {
    let two = #"[{"pid":4242,"command":"Codex"},{"pid":4243,"command":"Codex"}]"#
    let second = #"[{"pid":4243,"command":"Codex"}]"#
    let (model, executor, applications, trace) = makeModel([
      .init(command: .status, output: success(statusData(processes: two))),
      .init(command: .status, output: success(statusData(processes: two))),
      .init(command: .status, output: success(statusData(processes: two))),
      .init(command: .status, output: success(statusData(processes: two))),
      .init(command: .status, output: success(statusData(processes: two))),
      .init(command: .status, output: success(statusData(processes: second))),
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: switchOutput),
      .init(command: .status, output: success(statusData(active: "b"))),
    ])
    await model.beginSwitch(accountID: "b")
    await model.confirmQuit()
    await model.confirmForceQuit()
    #expect(applications.terminations.map { $0.0.process.pid } == [4242, 4243, 4242, 4243])
    #expect(
      trace.events == [
        "status", "status", "quit", "status", "quit", "status", "status", "force", "status",
        "force", "status", "use", "status",
      ])
    #expect(await executor.remaining == 0)
  }

  @Test func failedForceQuitNeverSwitchesOrEscalatesAgain() async {
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
    ])
    await model.beginSwitch(accountID: "b")
    await model.confirmQuit()
    await model.confirmForceQuit()
    #expect(model.phase == .awaitingManualExit)
    await model.confirmForceQuit()
    #expect(applications.terminations.map(\.1) == [false, true])
    #expect(await executor.commands.allSatisfy { $0 == .status })
    #expect(await executor.remaining == 0)
  }

  @Test func aliasIDCollisionStopsBeforeQuitting() async throws {
    let collision = try #require(String(data: statusData(processes: guiProcesses), encoding: .utf8))
      .replacingOccurrences(of: "\"alias\":\"personal\"", with: "\"alias\":\"a\"")
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(Data(collision.utf8)))
    ])
    await model.beginSwitch(accountID: "b")
    #expect(model.errorMessage == UIError.ambiguousAlias.errorDescription)
    #expect(model.phase == .idle)
    #expect(applications.terminations.isEmpty)
    #expect(await executor.commands == [.status])
  }

  @Test func openingMenuOnlyReadsCachedStatus() async {
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(statusData()))
    ])
    await model.menuOpened()
    #expect(await executor.commands.map(\.arguments) == [["ui", "status", "--json"]])
    #expect(model.status?.accounts.count == 2)
    #expect(applications.terminations.isEmpty)
    #expect(model.phase == .idle)
  }

  @Test func manualRefreshThenStatus() async throws {
    let (model, executor, _, _) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(command: .refreshUsage, output: success(try refreshedAccounts())),
      .init(command: .status, output: success(statusData())),
    ])
    await model.menuOpened()
    await model.refreshUsage()
    #expect(
      await executor.commands.map(\.arguments) == [
        ["ui", "status", "--json"], ["account", "list", "--refresh", "--json"],
        ["ui", "status", "--json"],
      ])
    #expect(model.errorMessage == nil)
  }

  @Test(arguments: [false, true])
  func failedRefreshOrStatusRetainsLastGoodState(statusFails: Bool) async throws {
    var steps: [ScriptedExecutor.Step] = [.init(command: .status, output: success(statusData()))]
    steps.append(
      .init(
        command: .refreshUsage,
        output: statusFails ? success(try refreshedAccounts()) : failedOutput()))
    if statusFails { steps.append(.init(command: .status, output: failedOutput())) }
    let (model, executor, _, _) = makeModel(steps)
    await model.menuOpened()
    let lastGood = model.status
    await model.refreshUsage()
    #expect(model.status == lastGood)
    #expect(model.errorMessage != nil)
    #expect(model.errorMessage?.contains(syntheticCanary) == false)
    #expect(model.phase == .idle)
    #expect(await executor.remaining == 0)
  }

  @Test func failedMenuReadRetainsLastGoodState() async {
    let (model, _, _, _) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(command: .status, output: failedOutput()),
    ])
    await model.menuOpened()
    let lastGood = model.status
    await model.menuOpened()
    #expect(model.status == lastGood)
    #expect(model.errorMessage?.contains(syntheticCanary) == false)
  }

  @Test func runningApplicationWaitsForConsentAndVerification() async {
    let (model, executor, applications, trace) = makeModel([
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: switchOutput),
      .init(command: .status, output: success(statusData(active: "b"))),
    ])
    await model.beginSwitch(accountID: "b")
    #expect(model.phase == .awaitingQuit)
    #expect(await executor.commands == [.status])
    #expect(applications.terminations.isEmpty)
    await model.refreshUsage()
    await model.confirmForceQuit()  // Cannot skip graceful consent.
    #expect(applications.terminations.isEmpty)
    await model.confirmQuit()
    #expect(applications.terminations.count == 1)
    #expect(applications.terminations.first?.1 == false)
    #expect(trace.events == ["status", "status", "quit", "status", "use", "status"])
    #expect(model.phase == .idle)
    #expect(await executor.remaining == 0)
  }

  @Test func forceQuitRequiresSecondConfirmationAndFreshStatus() async {
    let (model, executor, applications, trace) = makeModel([
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: switchOutput),
      .init(command: .status, output: success(statusData(active: "b"))),
    ])
    await model.beginSwitch(accountID: "b")
    await model.confirmQuit()
    #expect(model.phase == .awaitingForceQuit)
    #expect(applications.terminations.map(\.1) == [false])
    #expect(await executor.commands.allSatisfy { $0 == .status })
    await model.confirmForceQuit()
    #expect(applications.terminations.map(\.1) == [false, true])
    #expect(
      trace.events == [
        "status", "status", "quit", "status", "status", "force", "status", "use", "status",
      ])
    #expect(await executor.remaining == 0)
  }

  @Test(arguments: [false, true])
  func forceQuitNeverUsesStalePID(disappeared: Bool) async {
    var steps: [ScriptedExecutor.Step] = [
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
      .init(
        command: .status, output: success(statusData(processes: disappeared ? "[]" : guiProcesses))),
    ]
    if disappeared {
      steps += [
        .init(command: .status, output: success(statusData())),
        .init(command: .use("personal"), output: switchOutput),
        .init(command: .status, output: success(statusData(active: "b"))),
      ]
    }
    let (model, executor, applications, _) = makeModel(steps)
    await model.beginSwitch(accountID: "b")
    await model.confirmQuit()
    // Even an identical PID/command cannot transfer consent to a new app instance.
    applications.launchDate = Date(timeIntervalSince1970: 200)
    await model.confirmForceQuit()
    #expect(applications.terminations.map(\.1) == [false])
    #expect(model.phase == (disappeared ? .idle : .awaitingQuit))
    #expect(await executor.remaining == 0)
  }

  @Test func terminalProcessRequiresManualExitAndRetry() async {
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(statusData(processes: terminalProcesses))),
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: switchOutput),
      .init(command: .status, output: success(statusData(active: "b"))),
    ])
    await model.beginSwitch(accountID: "b")
    #expect(model.phase == .awaitingManualExit)
    await model.confirmQuit()
    await model.confirmForceQuit()
    #expect(applications.terminations.isEmpty)
    #expect(await executor.commands == [.status])
    await model.retrySwitch()
    #expect(model.phase == .idle)
    #expect(applications.terminations.isEmpty)
    #expect(await executor.remaining == 0)
  }

  @Test func useRaceReturnsToQuitFlowWithoutBypass() async {
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: failedOutput()),
      .init(command: .status, output: success(statusData(processes: guiProcesses))),
    ])
    await model.beginSwitch(accountID: "b")
    #expect(model.phase == .awaitingQuit)
    #expect(applications.terminations.isEmpty)
    #expect(await executor.commands == [.status, .use("personal"), .status])
    #expect(model.errorMessage?.contains(syntheticCanary) == false)
    model.cancelSwitch()
    #expect(model.phase == .idle)
  }

  @Test(arguments: ["unmanaged", "ambiguous", "credential_conflict"])
  func uncertainStateNeverSwitches(state: String) async {
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(statusData(state: state)))
    ])
    await model.beginSwitch(accountID: "b")
    #expect(model.phase == .idle)
    #expect(model.status?.activeAccountID == nil)
    #expect(model.errorMessage != nil)
    #expect(await executor.commands == [.status])
    #expect(applications.terminations.isEmpty)
  }

  @Test func concurrentActionsAreExcluded() async throws {
    let gate = ExecutionGate()
    let (model, executor, applications, _) = makeModel([
      .init(command: .refreshUsage, output: success(try refreshedAccounts()), gate: gate),
      .init(command: .status, output: success(statusData())),
    ])
    let refreshing = Task { await model.refreshUsage() }
    await gate.waitForEntry()
    #expect(model.phase == .refreshing)
    await model.refreshUsage()
    await model.beginSwitch(accountID: "b")
    await model.menuOpened()
    await model.openCodex()
    #expect(await executor.commands == [.refreshUsage])
    await gate.resume()
    await refreshing.value
    #expect(await executor.commands == [.refreshUsage, .status])
    #expect(applications.terminations.isEmpty)
  }

  @Test(arguments: [false, true])
  func switchingStaysLockedThroughStatusReload(statusFails: Bool) async {
    let useGate = ExecutionGate()
    let reloadGate = ExecutionGate()
    let (model, executor, applications, _) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: switchOutput, gate: useGate),
      .init(
        command: .status,
        output: statusFails ? failedOutput() : success(statusData(active: "b")),
        gate: reloadGate),
    ])
    await model.menuOpened()
    let lastGood = model.status
    var phases: [AppModel.Phase] = []
    let observation = model.$phase.dropFirst().sink { phases.append($0) }
    let switching = Task { await model.beginSwitch(accountID: "b") }
    await useGate.waitForEntry()
    #expect(model.phase == .switching)
    await model.beginSwitch(accountID: "a")
    await model.refreshUsage()
    await model.openCodex()
    await model.menuOpened()
    model.cancelSwitch()
    await useGate.resume()
    await reloadGate.waitForEntry()
    #expect(model.phase == .switching)
    #expect(!phases.contains(.idle))
    await model.beginSwitch(accountID: "a")
    await model.menuOpened()
    await model.refreshUsage()
    await model.openCodex()
    await reloadGate.resume()
    await switching.value
    #expect(model.phase == .idle)
    #expect(phases.last == .idle)
    #expect(await executor.commands == [.status, .status, .use("personal"), .status])
    #expect(applications.terminations.isEmpty)
    if statusFails {
      #expect(model.status == lastGood)
      #expect(model.errorMessage != nil)
    } else {
      #expect(model.status?.activeAccountID == "b")
      #expect(model.errorMessage == nil)
    }
    withExtendedLifetime(observation) {}
  }

  @Test(arguments: [false, true])
  func openingCodexReloadsCachedProcessStatus(statusFails: Bool) async {
    let (model, executor, _, trace) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(
        command: .status,
        output: statusFails ? failedOutput() : success(statusData(processes: guiProcesses))),
    ])
    await model.menuOpened()
    let previous = model.status
    await model.openCodex()
    #expect(model.phase == .idle)
    #expect(trace.events == ["status", "open", "status"])
    #expect(await executor.commands == [.status, .status])
    if statusFails {
      #expect(model.status == previous)
      #expect(model.errorMessage != nil)
    } else {
      #expect(model.status?.codexProcesses.count == 1)
      #expect(model.errorMessage == nil)
    }
  }

  @Test func nativeOpenFailureIsSafeAndIndependentOfSwitching() async {
    let (model, executor, applications, _) = makeModel([])
    applications.failOpen = true
    await model.openCodex()
    #expect(model.errorMessage == "Codex could not be opened.")
    #expect(await executor.commands.isEmpty)
    #expect(applications.terminations.isEmpty)
  }
}
