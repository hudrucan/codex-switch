import Foundation

public enum UIError: Error, LocalizedError, Equatable, Sendable {
  case cliUnavailable, unsafeExecutable, invalidResponse, unsupportedSchema, commandFailed,
    outputLimit
  case unsupportedAlias, ambiguousAlias, refreshFailed, openFailed

  public var errorDescription: String? {
    switch self {
    case .cliUnavailable:
      return
        "CLI unavailable. Bundle codex-switch or set CODEX_SWITCH_CLI_PATH to an absolute path."
    case .unsafeExecutable:
      return "The configured CLI path is not a trusted executable. Check ownership and permissions."
    case .invalidResponse:
      return "The CLI returned an invalid response. Check that it supports the UI bridge."
    case .unsupportedSchema:
      return "This CLI status version is not supported. Use a compatible codex-switch build."
    case .commandFailed:
      return "The CLI command failed. Review account state with the CLI and retry."
    case .outputLimit: return "The CLI response exceeded the supported size."
    case .unsupportedAlias:
      return "This alias cannot be switched from the menu. Rename it with the CLI."
    case .ambiguousAlias:
      return
        "This alias conflicts with another profile ID. Rename it with the CLI before switching."
    case .refreshFailed:
      return "Usage refresh failed for one or more accounts. The previous menu data is retained."
    case .openFailed:
      return "Codex could not be opened. Check that a supported application is installed."
    }
  }
}

public enum CLICommand: Equatable, Sendable {
  case status, refreshUsage
  case use(String)

  public var arguments: [String] {
    switch self {
    case .status: return ["ui", "status", "--json"]
    case .refreshUsage: return ["account", "list", "--refresh", "--json"]
    case .use(let alias): return ["use", alias, "--json"]
    }
  }
}

public struct CommandOutput: Sendable {
  public let stdout: Data
  public let stderr: Data
  public let exitStatus: Int32

  public init(stdout: Data, stderr: Data, exitStatus: Int32) {
    self.stdout = stdout
    self.stderr = stderr
    self.exitStatus = exitStatus
  }
}

public protocol ProcessExecuting: Sendable {
  func execute(_ command: CLICommand) async throws -> CommandOutput
}

public protocol CLIClientProtocol: Sendable {
  func status() async throws -> UIStatus
  func refreshUsage() async throws
  func use(alias: String) async throws
}

public struct CLIClient: CLIClientProtocol {
  private let executor: any ProcessExecuting

  public init(executor: any ProcessExecuting) { self.executor = executor }

  private func run(_ command: CLICommand) async throws -> Data {
    do {
      let result = try await executor.execute(command)
      guard result.exitStatus == 0 else { throw UIError.commandFailed }
      // Stderr is captured by the executor but never displayed or parsed.
      return result.stdout
    } catch let error as UIError {
      throw error
    } catch {
      throw UIError.commandFailed
    }
  }

  public func status() async throws -> UIStatus {
    try UIStatus.decode(await run(.status))
  }

  public func refreshUsage() async throws {
    let data = try await run(.refreshUsage)
    let accounts: [Account]
    do { accounts = try JSONDecoder().decode([Account].self, from: data) } catch {
      throw UIError.invalidResponse
    }
    // The CLI may exit successfully while reporting per-account failures.
    guard !accounts.contains(where: { $0.usage?.error?.isEmpty == false }) else {
      throw UIError.refreshFailed
    }
  }

  static func validateAlias(_ alias: String) throws {
    // Existing CLI aliases can begin with '-'. Never let one become a flag.
    guard !alias.isEmpty, !alias.hasPrefix("-"), alias.count <= 64,
      alias == alias.trimmingCharacters(in: .whitespacesAndNewlines),
      !alias.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) })
    else { throw UIError.unsupportedAlias }
  }

  public func use(alias: String) async throws {
    try Self.validateAlias(alias)
    let data = try await run(.use(alias))
    struct SwitchResult: Decodable {
      let changed: Bool
      let profileId: String
      let alias: String
    }
    do { _ = try JSONDecoder().decode(SwitchResult.self, from: data) } catch {
      throw UIError.invalidResponse
    }
  }
}
