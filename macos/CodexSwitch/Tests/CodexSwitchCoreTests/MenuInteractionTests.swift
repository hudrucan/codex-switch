import AppKit
import Testing

@testable import CodexSwitchApp

@Suite @MainActor
struct MenuInteractionTests {
  final class WindowDouble: NSWindow {
    var reportedVisible = true
    override var isVisible: Bool { reportedVisible }
  }

  @Test func visibleMenuRegainingFocusDoesNotStartAnotherStatusRead() async {
    var opens = 0
    var closes = 0
    let observer = MenuOpenObserver.OpenView(opened: { opens += 1 }, closed: { closes += 1 })
    let window = WindowDouble(
      contentRect: NSRect(x: 0, y: 0, width: 400, height: 40),
      styleMask: [.borderless], backing: .buffered, defer: false)
    window.isReleasedWhenClosed = false
    defer { window.close() }
    window.contentView = observer
    for _ in 0..<10 { await Task.yield() }
    #expect(opens == 1)
    let initialCloses = closes
    // Quit & Switch can change focus while the menu remains onscreen.
    NotificationCenter.default.post(name: NSWindow.didResignKeyNotification, object: window)
    NotificationCenter.default.post(name: NSWindow.didBecomeKeyNotification, object: window)
    for _ in 0..<10 { await Task.yield() }
    #expect(closes == initialCloses)
    #expect(opens == 1)
    // Actual hide/reopen transitions must still refresh status once per opening.
    window.reportedVisible = false
    NotificationCenter.default.post(
      name: NSWindow.didChangeOcclusionStateNotification, object: window)
    #expect(closes == initialCloses + 1)
    window.reportedVisible = true
    NotificationCenter.default.post(
      name: NSWindow.didChangeOcclusionStateNotification, object: window)
    NotificationCenter.default.post(name: NSWindow.didBecomeKeyNotification, object: window)
    for _ in 0..<10 { await Task.yield() }
    #expect(opens == 2)
  }

}
