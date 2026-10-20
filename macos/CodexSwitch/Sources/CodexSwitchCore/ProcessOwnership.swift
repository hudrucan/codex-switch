import Darwin
import Foundation

struct ProcessRecord: Equatable {
  let pid: Int32
  let parentPID: Int32
  let executablePath: String
  let startSeconds: UInt64
  let startMicroseconds: UInt64
  let hasTerminal: Bool

  static func read(_ pid: Int32) -> ProcessRecord? {
    guard pid > 0 else { return nil }
    var info = proc_bsdinfo()
    let size = Int32(MemoryLayout<proc_bsdinfo>.size)
    guard proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, size) == size,
      info.pbi_pid == UInt32(pid), info.pbi_ppid <= UInt32(Int32.max)
    else { return nil }
    var buffer = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
    guard proc_pidpath(pid, &buffer, UInt32(buffer.count)) > 0 else { return nil }
    guard
      let path = String(
        bytes: buffer.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, encoding: .utf8),
      path.hasPrefix("/")
    else { return nil }
    return ProcessRecord(
      pid: pid, parentPID: Int32(info.pbi_ppid),
      executablePath: URL(fileURLWithPath: path).resolvingSymlinksInPath().standardizedFileURL.path,
      startSeconds: info.pbi_start_tvsec, startMicroseconds: info.pbi_start_tvusec,
      hasTerminal: info.e_tdev != UInt32.max
    )
  }
}

struct GUIApplicationRecord: Equatable {
  let target: ApplicationTarget
  let bundlePath: String
  let executablePath: String
}

// Ownership routes a CLI-reported process to a GUI; it never authorizes sending
// a signal to that child. Unknown ancestry and terminal sessions fail closed.
@MainActor
struct GUIProcessResolver {
  let record: (Int32) -> ProcessRecord?
  let application: (Int32) -> GUIApplicationRecord?

  func resolve(_ process: CodexProcess) -> ApplicationTarget? {
    guard ["Codex", "ChatGPT", "codex"].contains(process.command), process.pid > 0 else {
      return nil
    }
    var lineage: [ProcessRecord] = []
    var seen: Set<Int32> = []
    var pid = process.pid
    while pid > 1 && lineage.count < 32 && seen.insert(pid).inserted {
      guard let node = record(pid), node.pid == pid, !node.hasTerminal else { return nil }
      lineage.append(node)
      if let owner = application(pid) {
        guard owner.target.process.pid == pid, node.executablePath == owner.executablePath,
          lineage.allSatisfy({ child in
            child.executablePath.hasPrefix(owner.bundlePath + "/")
              && (child.startSeconds > node.startSeconds
                || (child.startSeconds == node.startSeconds
                  && child.startMicroseconds >= node.startMicroseconds))
          }),
          // Re-read the complete chain to reject reparenting, exec and PID reuse.
          lineage.allSatisfy({ record($0.pid) == $0 }), application(pid) == owner
        else { return nil }
        return owner.target
      }
      pid = node.parentPID
    }
    return nil
  }
}
