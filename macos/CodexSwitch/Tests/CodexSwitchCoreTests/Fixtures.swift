import Foundation
import Testing

@testable import CodexSwitchCore

let guiProcesses = #"[{"pid":4242,"command":"Codex"}]"#
let terminalProcesses = #"[{"pid":4343,"command":"codex"}]"#
let syntheticCanary = "SYNTHETIC_RAW_DIAGNOSTIC_CANARY"

func statusData(processes: String = "[]", state: String = "in_sync", active: String = "a") -> Data {
  Data(
    """
    {
      "schemaVersion": 1,
      "futureTopLevel": {"ignored": true},
      "accounts": [
        {"id":"a","alias":"work","accountId":"account-a","source":"synthetic",
         "workspaceId":"workspace-a","active":\(active == "a"),
         "usage":{"status":"stale","fetchedAt":"2026-10-20T01:02:03.123456789Z","planType":"pro",
           "rateLimits":{"rateLimits":{"primary":{"usedPercent":38,"windowDurationMins":300,"resetsAt":1792483200},
             "secondary":{"usedPercent":59,"windowDurationMins":10080,"resetsAt":1792742400}},
             "rateLimitResetCredits":{"availableCount":2},"futureField":true}}},
        {"id":"b","alias":"personal","accountId":"account-b","source":"synthetic","active":\(active == "b")}
      ],
      "observation":{"state":"\(state)","hasLive":true,"managed":true,"needsSync":false,
        "profileId":"\(active)","alias":"\(active == "a" ? "work" : "personal")","accountId":"account-\(active)"},
      "codexProcesses": \(processes)
    }
    """.utf8)
}

func freshStatusData(
  processes: String = "[]", state: String = "in_sync", active: String = "a",
  fetchedAt: Date = Date()
) -> Data {
  var root =
    try! JSONSerialization.jsonObject(
      with: statusData(processes: processes, state: state, active: active)) as! [String: Any]
  var accounts = root["accounts"] as! [[String: Any]]
  var usage = accounts[0]["usage"] as! [String: Any]
  usage["status"] = "fresh"
  usage["fetchedAt"] = ISO8601DateFormatter().string(from: fetchedAt)
  for index in accounts.indices { accounts[index]["usage"] = usage }
  root["accounts"] = accounts
  return try! JSONSerialization.data(withJSONObject: root)
}

func success(_ data: Data) -> CommandOutput {
  CommandOutput(stdout: data, stderr: Data(), exitStatus: 0)
}
func failedOutput() -> CommandOutput {
  CommandOutput(
    stdout: Data("invalid JSON".utf8), stderr: Data(syntheticCanary.utf8), exitStatus: 1)
}

func refreshedAccounts() throws -> Data {
  let root = try #require(JSONSerialization.jsonObject(with: statusData()) as? [String: Any])
  return try JSONSerialization.data(withJSONObject: #require(root["accounts"]))
}

let switchOutput = success(Data(#"{"changed":true,"profileId":"b","alias":"personal"}"#.utf8))

@MainActor
final class ActionTrace {
  var events: [String] = []
  func append(_ event: String) { events.append(event) }
}

actor ExecutionGate {
  private var entered = false
  private var entry: CheckedContinuation<Void, Never>?
  private var release: CheckedContinuation<Void, Never>?

  func pause() async {
    entered = true
    entry?.resume()
    entry = nil
    await withCheckedContinuation { release = $0 }
  }

  func waitForEntry() async {
    if entered { return }
    await withCheckedContinuation { entry = $0 }
  }

  func resume() {
    release?.resume()
    release = nil
  }
}

actor ScriptedExecutor: ProcessExecuting {
  struct Step: Sendable {
    let command: CLICommand
    let output: CommandOutput
    var gate: ExecutionGate?
  }

  private var steps: [Step]
  private let trace: ActionTrace
  private(set) var commands: [CLICommand] = []

  init(_ steps: [Step], trace: ActionTrace) {
    self.steps = steps
    self.trace = trace
  }

  func execute(_ command: CLICommand) async throws -> CommandOutput {
    commands.append(command)
    await trace.append(command == .status ? "status" : command == .refreshUsage ? "refresh" : "use")
    guard !steps.isEmpty else {
      Issue.record("Unexpected extra CLI invocation")
      throw UIError.commandFailed
    }
    let step = steps.removeFirst()
    #expect(command == step.command)
    if let gate = step.gate { await gate.pause() }
    return step.output
  }

  var remaining: Int { steps.count }
}

@MainActor
final class ApplicationDouble: ApplicationControlling {
  let trace: ActionTrace
  var launchDate = Date(timeIntervalSince1970: 100)
  var terminations: [(ApplicationTarget, Bool)] = []
  var failOpen = false
  var ownerPIDs: [Int32: Int32] = [:]
  var exitOnQuit = false
  var unavailableOwners: Set<Int32> = []

  init(trace: ActionTrace) { self.trace = trace }

  func identify(_ process: CodexProcess) -> ApplicationTarget? {
    guard let pid = ownerPIDs[process.pid] ?? (process.command == "Codex" ? process.pid : nil),
      !unavailableOwners.contains(pid)
    else { return nil }
    return ApplicationTarget(
      process: CodexProcess(pid: pid, command: "Codex"),
      bundleIdentifier: "synthetic.codex", launchDate: launchDate)
  }

  func requestTermination(_ target: ApplicationTarget, force: Bool) -> Bool {
    terminations.append((target, force))
    trace.append(force ? "force" : "quit")
    if exitOnQuit { unavailableOwners.insert(target.process.pid) }
    return true
  }

  func openCodex() async throws {
    if failOpen { throw NSError(domain: syntheticCanary, code: 1) }
    trace.append("open")
  }
}

@MainActor
func makeModel(
  _ steps: [ScriptedExecutor.Step], quitPollAttempts: Int = 1,
  now: @escaping () -> Date = { Date() }
) -> (
  AppModel, ScriptedExecutor, ApplicationDouble, ActionTrace
) {
  let trace = ActionTrace()
  let executor = ScriptedExecutor(steps, trace: trace)
  let applications = ApplicationDouble(trace: trace)
  let model = AppModel(
    client: CLIClient(executor: executor), applications: applications,
    quitPollAttempts: quitPollAttempts, now: now, wait: {}
  )
  return (model, executor, applications, trace)
}
