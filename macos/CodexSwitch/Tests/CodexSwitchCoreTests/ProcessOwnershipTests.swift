import Foundation
import Testing

@testable import CodexSwitchCore

@Suite @MainActor
struct ProcessOwnershipTests {
  private let bundle = "/Applications/Synthetic.app"

  private var gui: GUIApplicationRecord {
    GUIApplicationRecord(
      target: ApplicationTarget(
        process: CodexProcess(pid: 10, command: "ChatGPT"), bundleIdentifier: "synthetic.gui",
        launchDate: Date(timeIntervalSince1970: 100)),
      bundlePath: bundle, executablePath: bundle + "/Contents/MacOS/ChatGPT")
  }

  private func node(
    _ pid: Int32, parent: Int32, path: String, start: UInt64 = 110, terminal: Bool = false
  ) -> ProcessRecord {
    ProcessRecord(
      pid: pid, parentPID: parent, executablePath: path,
      startSeconds: start, startMicroseconds: 0, hasTerminal: terminal)
  }

  private var records: [Int32: ProcessRecord] {
    [
      10: node(10, parent: 1, path: gui.executablePath, start: 100),
      20: node(20, parent: 10, path: bundle + "/Contents/Frameworks/Renderer"),
      30: node(30, parent: 20, path: bundle + "/Contents/Resources/codex"),
    ]
  }

  @Test func bundledDescendantsResolveToOneVerifiedGUI() {
    let resolver = GUIProcessResolver(
      record: { records[$0] }, application: { $0 == 10 ? gui : nil })
    for (pid, command): (Int32, String) in [(10, "ChatGPT"), (20, "Codex"), (30, "codex")] {
      #expect(resolver.resolve(CodexProcess(pid: pid, command: command)) == gui.target)
    }
  }

  @Test func terminalAndExternalExecutablesNeverInheritGUIConsent() {
    for replacement in [
      node(30, parent: 20, path: bundle + "/Contents/Resources/codex", terminal: true),
      node(30, parent: 20, path: "/usr/local/bin/codex"),
      node(30, parent: 20, path: bundle + ".spoof/Contents/codex"),
      node(30, parent: 1, path: bundle + "/Contents/Resources/codex"),
      node(30, parent: 20, path: bundle + "/Contents/Resources/codex", start: 99),
    ] {
      var snapshot = records
      snapshot[30] = replacement
      let resolver = GUIProcessResolver(
        record: { snapshot[$0] }, application: { $0 == 10 ? gui : nil })
      #expect(resolver.resolve(CodexProcess(pid: 30, command: "codex")) == nil)
    }
  }

  @Test func missingMetadataUnknownGUIAndAncestryCyclesFailClosed() {
    var cycle = records
    cycle[20] = node(20, parent: 30, path: bundle + "/Contents/Frameworks/Renderer")
    for snapshot in [records.filter { $0.key != 20 }, cycle] {
      let resolver = GUIProcessResolver(
        record: { snapshot[$0] }, application: { $0 == 10 ? gui : nil })
      #expect(resolver.resolve(CodexProcess(pid: 30, command: "codex")) == nil)
    }
    let unverified = GUIProcessResolver(record: { records[$0] }, application: { _ in nil })
    #expect(unverified.resolve(CodexProcess(pid: 30, command: "codex")) == nil)
  }

  @Test func changingAncestryExecutableOrPIDGenerationFailsReverification() {
    for changed in [
      node(30, parent: 20, path: bundle + "/Contents/Resources/codex", start: 120),
      node(30, parent: 1, path: bundle + "/Contents/Resources/codex"),
      node(30, parent: 20, path: "/usr/local/bin/codex"),
    ] {
      var reads = 0
      let resolver = GUIProcessResolver(
        record: { pid in
          if pid == 30 {
            reads += 1
            if reads > 1 { return changed }
          }
          return records[pid]
        }, application: { $0 == 10 ? gui : nil })
      #expect(resolver.resolve(CodexProcess(pid: 30, command: "codex")) == nil)
    }
  }

  @Test func changedNativeAppInstanceDoesNotInheritConsent() {
    var reads = 0
    let resolver = GUIProcessResolver(
      record: { records[$0] },
      application: { pid in
        guard pid == 10 else { return nil }
        reads += 1
        guard reads > 1 else { return gui }
        return GUIApplicationRecord(
          target: ApplicationTarget(
            process: gui.target.process, bundleIdentifier: "synthetic.gui",
            launchDate: Date(timeIntervalSince1970: 120)),
          bundlePath: bundle, executablePath: gui.executablePath)
      })
    #expect(resolver.resolve(CodexProcess(pid: 30, command: "codex")) == nil)
  }
}
