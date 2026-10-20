import Darwin
import Foundation

public struct CLIResolver: Sendable {
  private let configuredPath: String?
  private let bundleURL: URL

  public init(configuredPath: String?, bundleURL: URL) {
    self.configuredPath = configuredPath
    self.bundleURL = bundleURL
  }

  public func resolve() throws -> URL {
    let candidate: URL
    if let configuredPath {
      guard configuredPath.hasPrefix("/") else { throw UIError.cliUnavailable }
      candidate = URL(fileURLWithPath: configuredPath)
    } else {
      candidate = bundleURL.appendingPathComponent("Contents/Helpers/codex-switch")
    }
    let resolved = candidate.standardizedFileURL.resolvingSymlinksInPath()
    let files = FileManager.default
    do {
      let attributes = try files.attributesOfItem(atPath: resolved.path)
      guard attributes[.type] as? FileAttributeType == .typeRegular,
        files.isExecutableFile(atPath: resolved.path),
        let permissions = attributes[.posixPermissions] as? NSNumber,
        permissions.intValue & 0o6000 == 0
      else { throw UIError.unsafeExecutable }
      var current = resolved
      while true {
        let info = try files.attributesOfItem(atPath: current.path)
        guard let owner = info[.ownerAccountID] as? NSNumber,
          owner.uint32Value == 0 || owner.uint32Value == getuid(),
          let mode = info[.posixPermissions] as? NSNumber,
          mode.intValue & 0o022 == 0
        else { throw UIError.unsafeExecutable }
        if current.path == "/" { break }
        current.deleteLastPathComponent()
      }
      return resolved
    } catch let error as UIError {
      throw error
    } catch {
      throw UIError.cliUnavailable
    }
  }
}

// Only the executor owns these child processes. It never signals a PID from status.
public struct CLIProcessExecutor: ProcessExecuting {
  private let resolver: CLIResolver

  public init(resolver: CLIResolver) { self.resolver = resolver }

  public func execute(_ command: CLICommand) async throws -> CommandOutput {
    let executable = try resolver.resolve()
    return try await withCheckedThrowingContinuation { continuation in
      DispatchQueue.global(qos: .userInitiated).async {
        do {
          let process = Process()
          let stdout = Pipe()
          let stderr = Pipe()
          process.executableURL = executable
          process.arguments = command.arguments
          process.standardInput = FileHandle.nullDevice
          process.standardOutput = stdout
          process.standardError = stderr
          let output = BoundedCapture(limit: 4 * 1024 * 1024)
          let diagnostics = BoundedCapture(limit: 64 * 1024)
          try process.run()
          // Drain both pipes concurrently; neither can block the child.
          let readers = DispatchGroup()
          for (pipe, capture) in [(stdout, output), (stderr, diagnostics)] {
            readers.enter()
            DispatchQueue.global(qos: .utility).async {
              capture.read(pipe.fileHandleForReading, process: process)
              readers.leave()
            }
          }
          // Bound informational reads used by the quit verification loop.
          // Mutating commands finish under the CLI's own lifecycle controls.
          let timeout = DispatchWorkItem {
            if process.isRunning { process.terminate() }
          }
          if command == .status {
            DispatchQueue.global().asyncAfter(deadline: .now() + 5, execute: timeout)
          }
          process.waitUntilExit()
          timeout.cancel()
          readers.wait()
          guard !output.exceeded && !diagnostics.exceeded else { throw UIError.outputLimit }
          continuation.resume(
            returning: CommandOutput(
              stdout: output.data, stderr: diagnostics.data, exitStatus: process.terminationStatus
            ))
        } catch let error as UIError {
          continuation.resume(throwing: error)
        } catch {
          continuation.resume(throwing: UIError.commandFailed)
        }
      }
    }
  }
}

private final class BoundedCapture: @unchecked Sendable {
  private let lock = NSLock()
  private let limit: Int
  private var bytes = Data()
  private var overflow = false

  init(limit: Int) { self.limit = limit }

  var data: Data { lock.withLock { bytes } }
  var exceeded: Bool { lock.withLock { overflow } }

  func read(_ handle: FileHandle, process: Process) {
    defer { try? handle.close() }
    while let chunk = try? handle.read(upToCount: 8192), !chunk.isEmpty {
      let shouldStop = lock.withLock {
        if bytes.count + chunk.count > limit {
          overflow = true
          return true
        }
        bytes.append(chunk)
        return false
      }
      if shouldStop {
        if process.isRunning { process.terminate() }
        // Keep draining after the limit, without retaining further bytes.
        while let rest = try? handle.read(upToCount: 8192), !rest.isEmpty {}
        return
      }
    }
  }
}
