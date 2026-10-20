import AppKit
import CodexSwitchCore
import SwiftUI

@main
struct CodexSwitchApp: App {
  @NSApplicationDelegateAdaptor(AgentDelegate.self) private var delegate
  @StateObject private var model: AppModel

  init() {
    let bundle = Bundle.main
    let resolver = CLIResolver(
      configuredPath: ProcessInfo.processInfo.environment["CODEX_SWITCH_CLI_PATH"],
      bundleURL: bundle.bundleURL
    )
    let identifiers =
      bundle.object(forInfoDictionaryKey: "CodexApplicationBundleIdentifiers") as? [String] ?? []
    _model = StateObject(
      wrappedValue: AppModel(
        client: CLIClient(executor: CLIProcessExecutor(resolver: resolver)),
        applications: MacApplications(bundleIdentifiers: identifiers)
      ))
  }

  var body: some Scene {
    MenuBarExtra("Codex Switch", systemImage: "person.crop.circle") {
      SwitchMenu(model: model)
    }
    .menuBarExtraStyle(.window)
  }
}

final class AgentDelegate: NSObject, NSApplicationDelegate {
  func applicationDidFinishLaunching(_ notification: Notification) {
    NSApplication.shared.setActivationPolicy(.accessory)
  }
}

private struct SwitchMenu: View {
  @ObservedObject var model: AppModel

  var body: some View {
    VStack(alignment: .leading, spacing: 10) {
      Text("Codex Switch").font(.headline)
      Divider()
      if let status = model.status {
        if let notice = status.observationNotice {
          Text(notice).font(.caption).foregroundStyle(.secondary)
        }
        if status.accounts.isEmpty {
          Text("No accounts saved. Add an account with the CLI.")
            .font(.callout).foregroundStyle(.secondary)
        } else {
          ScrollView {
            VStack(alignment: .leading, spacing: 4) {
              ForEach(status.accounts) { account in
                accountRow(account, active: status.activeAccountID == account.id)
                  .disabled(
                    model.isBusy || !status.canSwitch || status.activeAccountID == account.id)
              }
            }
          }
          .frame(maxHeight: 280)
        }
        if !status.codexProcesses.isEmpty {
          Text("Codex is running (\(status.codexProcesses.count) processes).")
            .font(.caption).foregroundStyle(.secondary)
        }
      } else if model.progressLabel == nil {
        Text("Status unavailable. Check the CLI configuration and retry.")
          .font(.callout).foregroundStyle(.secondary)
      }
      if let message = model.errorMessage {
        Text(message).font(.caption).foregroundStyle(.red)
        if model.status != nil {
          Text("Showing the last successful status.").font(.caption).foregroundStyle(.secondary)
        }
      }
      if let progress = model.progressLabel {
        HStack {
          ProgressView().controlSize(.small)
          Text(progress).font(.caption)
        }
      }
      switchConfirmation
      Divider()
      Button("Refresh Usage") { Task { await model.refreshUsage() } }
        .disabled(model.isBusy)
      Button("Retry Status") { Task { await model.menuOpened() } }
        .disabled(model.isBusy)
      Button("Open Codex") { Task { await model.openCodex() } }
        .disabled(model.isBusy)
      Divider()
      Button("Quit Codex Switch") { NSApplication.shared.terminate(nil) }
        .disabled(model.isBusy)
    }
    .padding(14)
    .frame(width: 340)
    .background(MenuOpenObserver { Task { await model.menuOpened() } })
  }

  private func accountRow(_ account: Account, active: Bool) -> some View {
    Button {
      Task { await model.beginSwitch(accountID: account.id) }
    } label: {
      HStack(alignment: .top, spacing: 8) {
        Image(systemName: active ? "checkmark" : "circle")
          .foregroundStyle(active ? Color.accentColor : Color.secondary)
          .frame(width: 16)
          .accessibilityLabel(active ? "Active account" : "Saved account")
        VStack(alignment: .leading, spacing: 3) {
          Text(account.alias).font(.body).lineLimit(1)
          Text(account.usage?.summary ?? "Usage unavailable")
            .font(.caption)
            .foregroundStyle(
              account.usage?.isStaleOrUnavailable == false ? .secondary : Color.orange)
          Text(account.usage?.updatedLabel ?? "No cached update time")
            .font(.caption2).foregroundStyle(.secondary)
        }
        Spacer(minLength: 0)
      }
      .padding(.vertical, 5)
      .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
    .help(active ? "Observed active account" : "Switch to \(account.alias)")
  }

  @ViewBuilder
  private var switchConfirmation: some View {
    if model.canCancel {
      Divider()
      Text("Switch to \(model.pendingAlias ?? "selected account")")
        .font(.subheadline).bold()
      if let processes = model.status?.codexProcesses {
        ForEach(processes, id: \.self) { process in
          Text("\(process.command) · PID \(process.pid)").font(.caption)
        }
      }
      switch model.phase {
      case .awaitingQuit:
        Text("Save your work. Quit Codex before switching?")
          .font(.caption)
        Button("Quit Codex & Switch") { Task { await model.confirmQuit() } }
      case .awaitingForceQuit:
        Text(
          "Codex has not quit. Force Quit may lose unsaved work. Confirm separately to continue."
        )
        .font(.caption).foregroundStyle(.orange)
        Button("Force Quit Codex", role: .destructive) { Task { await model.confirmForceQuit() } }
      case .awaitingManualExit:
        Text(
          "A Codex CLI or unverified application is still running. Close it yourself, then Retry. No terminal processes will be terminated."
        )
        .font(.caption)
        Button("Retry Switch") { Task { await model.retrySwitch() } }
      default: EmptyView()
      }
      Button("Cancel Switch", role: .cancel) { model.cancelSwitch() }
    }
  }
}

// Window-style MenuBarExtra content can survive being closed. Observe its actual
// window becoming key instead of relying on SwiftUI view construction as "open".
private struct MenuOpenObserver: NSViewRepresentable {
  let opened: @MainActor () -> Void

  func makeNSView(context: Context) -> OpenView { OpenView(opened: opened) }
  func updateNSView(_ view: OpenView, context: Context) { view.opened = opened }

  final class OpenView: NSView {
    var opened: @MainActor () -> Void

    init(opened: @escaping @MainActor () -> Void) {
      self.opened = opened
      super.init(frame: .zero)
    }

    required init?(coder: NSCoder) { nil }

    override func viewDidMoveToWindow() {
      super.viewDidMoveToWindow()
      NotificationCenter.default.removeObserver(self)
      guard let window else { return }
      NotificationCenter.default.addObserver(
        self, selector: #selector(windowOpened), name: NSWindow.didBecomeKeyNotification,
        object: window
      )
      if window.isVisible { opened() }
    }

    @objc private func windowOpened() { opened() }

    deinit { NotificationCenter.default.removeObserver(self) }
  }
}
