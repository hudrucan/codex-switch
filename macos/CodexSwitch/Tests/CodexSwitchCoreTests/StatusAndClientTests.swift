import Foundation
import Testing

@testable import CodexSwitchCore

struct StatusTests {
  @Test func decodesBridgeAndIgnoresFutureFields() throws {
    let status = try UIStatus.decode(statusData(processes: guiProcesses))
    #expect(status.schemaVersion == 1)
    #expect(status.accounts.count == 2)
    #expect(status.activeAccountID == "a")
    #expect(status.accounts[0].active)
    #expect(status.observation.workspaceId == nil)
    #expect(status.accounts[0].workspaceId == "workspace-a")
    #expect(status.codexProcesses == [CodexProcess(pid: 4242, command: "Codex")])
    let usage = try #require(status.accounts[0].usage)
    #expect(usage.planType == "pro")
    #expect(usage.rateLimits?.main.primary?.usedPercent == 38)
    #expect(usage.rateLimits?.main.secondary?.usedPercent == 59)
    #expect(usage.isStaleOrUnavailable)
    #expect(usage.fetchedAt == "2026-10-20T01:02:03.123456789Z")
  }

  @Test func unsupportedVersionIsRejectedBeforeItsPayload() {
    #expect(throws: UIError.unsupportedSchema) {
      try UIStatus.decode(Data(#"{"schemaVersion":2,"differentPayload":true}"#.utf8))
    }
  }

  @Test(arguments: ["unmanaged", "ambiguous", "credential_conflict", "future_state"])
  func uncertainObservationIsNotSafe(state: String) throws {
    let status = try UIStatus.decode(statusData(state: state))
    #expect(!status.canSwitch)
    #expect(status.activeAccountID == nil)
    #expect(status.observationNotice != nil)
  }

  @Test func malformedResponseHasSafeError() {
    #expect(throws: UIError.invalidResponse) { try UIStatus.decode(Data(syntheticCanary.utf8)) }
    #expect(!UIError.invalidResponse.localizedDescription.contains(syntheticCanary))
  }
}

@Suite @MainActor
struct ClientTests {
  @Test func exactCommandsAndNoProcessBypass() async throws {
    let (model, executor, _, _) = makeModel([
      .init(command: .status, output: success(statusData())),
      .init(command: .use("personal"), output: switchOutput),
      .init(command: .status, output: success(statusData(active: "b"))),
    ])
    await model.beginSwitch(accountID: "b")
    let commands = await executor.commands
    #expect(
      commands.map(\.arguments) == [
        ["ui", "status", "--json"], ["use", "personal", "--json"], ["ui", "status", "--json"],
      ])
    #expect(commands.allSatisfy { !$0.arguments.contains("--allow-running") })
    #expect(model.status?.activeAccountID == "b")
  }

  @Test(arguments: ["--allow-running", "--help", "-a", "", " invalid", "line\nbreak"])
  func aliasesCannotBecomeFlags(alias: String) async throws {
    let trace = ActionTrace()
    let executor = ScriptedExecutor([], trace: trace)
    let client = CLIClient(executor: executor)
    await #expect(throws: UIError.unsupportedAlias) { try await client.use(alias: alias) }
    #expect(await executor.commands.isEmpty)
  }

  @Test func nonzeroExitDoesNotDecodeOrDisplayOutput() async {
    let trace = ActionTrace()
    let executor = ScriptedExecutor([.init(command: .status, output: failedOutput())], trace: trace)
    let client = CLIClient(executor: executor)
    await #expect(throws: UIError.commandFailed) { try await client.status() }
    #expect(!UIError.commandFailed.localizedDescription.contains(syntheticCanary))
  }

  @Test(arguments: ["error", "id", "account", "workspace", "missing", "stale", "invalid-date"])
  func invalidRefreshResultsAreRejected(fault: String) async throws {
    let target = try #require(UIStatus.decode(freshStatusData()).accounts.first)
    var account = try #require(
      JSONSerialization.jsonObject(with: refreshedAccount()) as? [String: Any])
    var usage = try #require(account["usage"] as? [String: Any])
    switch fault {
    case "id": account["id"] = "other-profile"
    case "account": account["accountId"] = "other-account"
    case "workspace": account["workspaceId"] = "other-workspace"
    case "missing": account.removeValue(forKey: "usage")
    case "stale": usage["status"] = "stale"
    case "invalid-date": usage["fetchedAt"] = "invalid"
    default: usage["error"] = syntheticCanary
    }
    if fault != "missing" { account["usage"] = usage }
    let executor = ScriptedExecutor(
      [
        .init(
          command: .refreshUsage("work"),
          output: success(try JSONSerialization.data(withJSONObject: account)))
      ], trace: ActionTrace())
    do {
      _ = try await CLIClient(executor: executor).refreshUsage(account: target)
      Issue.record("Invalid refresh result was accepted")
    } catch {
      #expect(error is UIError)
      #expect(!error.localizedDescription.contains(syntheticCanary))
    }
    #expect(
      await executor.commands.map(\.arguments) == [["account", "usage", "--json", "--", "work"]])
  }

  @Test func explicitResolverNeverFallsBackToPath() throws {
    let bundle = URL(fileURLWithPath: "/nonexistent/Codex Switch.app")
    let explicit = CLIResolver(configuredPath: "/usr/bin/true", bundleURL: bundle)
    #expect(try explicit.resolve().path == "/usr/bin/true")
    #expect(throws: UIError.cliUnavailable) {
      try CLIResolver(configuredPath: "codex-switch", bundleURL: bundle).resolve()
    }
    #expect(throws: UIError.cliUnavailable) {
      try CLIResolver(configuredPath: nil, bundleURL: bundle).resolve()
    }
  }

  @Test func resolverRejectsSharedWritableDirectory() throws {
    let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    defer { try? FileManager.default.removeItem(at: root) }
    let binary = root.appendingPathComponent("synthetic-cli")
    try Data("synthetic; never executed".utf8).write(to: binary)
    try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: binary.path)
    try FileManager.default.setAttributes([.posixPermissions: 0o777], ofItemAtPath: root.path)
    #expect(throws: UIError.unsafeExecutable) {
      try CLIResolver(configuredPath: binary.path, bundleURL: root).resolve()
    }
  }
}
