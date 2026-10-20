import AppKit
import Foundation

public struct ApplicationTarget: Equatable, Hashable, Sendable {
  public let process: CodexProcess
  public let bundleIdentifier: String
  public let launchDate: Date

  public init(process: CodexProcess, bundleIdentifier: String, launchDate: Date) {
    self.process = process
    self.bundleIdentifier = bundleIdentifier
    self.launchDate = launchDate
  }
}

@MainActor
public protocol ApplicationControlling {
  func identify(_ process: CodexProcess) -> ApplicationTarget?
  func requestTermination(_ target: ApplicationTarget, force: Bool) -> Bool
  func openCodex() async throws
}

@MainActor
public final class MacApplications: ApplicationControlling {
  private let bundleIdentifiers: [String]
  private var knownApplications: [ApplicationTarget: NSRunningApplication] = [:]

  public init(bundleIdentifiers: [String]) { self.bundleIdentifiers = bundleIdentifiers }

  public func identify(_ process: CodexProcess) -> ApplicationTarget? {
    // This is UI routing only. The CLI remains the source of process classification.
    guard ["Codex", "ChatGPT"].contains(process.command),
      process.pid > 0, process.pid != ProcessInfo.processInfo.processIdentifier,
      let application = NSRunningApplication(processIdentifier: process.pid),
      !application.isTerminated,
      application.activationPolicy != .prohibited,
      let bundleID = application.bundleIdentifier, bundleIdentifiers.contains(bundleID),
      let launched = application.launchDate
    else { return nil }
    let target = ApplicationTarget(
      process: process, bundleIdentifier: bundleID, launchDate: launched)
    knownApplications = knownApplications.filter { !$0.value.isTerminated }
    if knownApplications[target] == nil { knownApplications[target] = application }
    return target
  }

  public func requestTermination(_ target: ApplicationTarget, force: Bool) -> Bool {
    guard let original = knownApplications[target],
      let current = NSRunningApplication(processIdentifier: target.process.pid),
      original == current, !current.isTerminated,
      identify(target.process) == target
    else { return false }
    return force ? current.forceTerminate() : current.terminate()
  }

  public func openCodex() async throws {
    let workspace = NSWorkspace.shared
    guard
      let url = bundleIdentifiers.lazy.compactMap({
        workspace.urlForApplication(withBundleIdentifier: $0)
      }).first
    else { throw UIError.openFailed }
    let configuration = NSWorkspace.OpenConfiguration()
    configuration.activates = true
    do {
      _ = try await workspace.openApplication(at: url, configuration: configuration)
    } catch {
      throw UIError.openFailed
    }
  }
}
