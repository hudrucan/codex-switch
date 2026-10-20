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
    MenuBarExtra("Codex Switch", systemImage: "square.on.square") {
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

struct SwitchMenu: View {
  @ObservedObject var model: AppModel

  private var activeAccount: Account? {
    guard let status = model.status else { return nil }
    return status.accounts.first { $0.id == status.activeAccountID }
  }

  var body: some View {
    VStack(alignment: .leading, spacing: 0) {
      HStack {
        Text("Codex Switch").font(.system(size: 13, weight: .semibold))
        Spacer()
        if let count = model.status?.accounts.count {
          Text("\(count) \(count == 1 ? "account" : "accounts")")
            .font(.system(size: 11)).foregroundStyle(.secondary)
        }
      }
      .padding(18)

      if let status = model.status {
        if let notice = status.observationNotice {
          noticeText(notice).padding(.horizontal, 18).padding(.bottom, 14)
        }
        if let account = activeAccount {
          currentAccount(account)
        }
        savedAccounts(status)
        if let account = activeAccount {
          Label(UsagePresentation(account.usage).cacheLabel, systemImage: "clock")
            .font(.system(size: 11))
            .foregroundStyle(
              account.usage?.isStaleOrUnavailable == false ? Color.secondary : .orange
            )
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 18).padding(.top, 5).padding(.bottom, 14)
        }
      } else if model.progressLabel == nil {
        noticeText("Status unavailable. Check the CLI configuration and retry.")
          .padding(.horizontal, 18).padding(.bottom, 14)
      }

      if let message = model.errorMessage {
        VStack(alignment: .leading, spacing: 4) {
          Text(message).foregroundStyle(.red)
          if model.status != nil {
            Text("Showing the last successful status.").foregroundStyle(.secondary)
          }
        }
        .font(.system(size: 11)).fixedSize(horizontal: false, vertical: true)
        .padding(.horizontal, 18).padding(.bottom, 14)
      }
      switchConfirmation
      Divider()
      VStack(spacing: 0) {
        MenuAction("Refresh Usage", symbol: "arrow.clockwise") {
          Task { await model.refreshUsage() }
        }
        if model.errorMessage != nil || model.status == nil {
          MenuAction("Retry Status", symbol: "arrow.clockwise.circle") {
            Task { await model.menuOpened() }
          }
        }
        MenuAction("Open Codex", symbol: "arrow.up.forward.square", opensApplication: true) {
          Task { await model.openCodex() }
        }
      }
      .disabled(model.isBusy)
      .opacity(model.canCancel ? 0.5 : 1)
      .padding(.horizontal, 8).padding(.vertical, 6)
      Divider()
      footer
    }
    .frame(width: 332)
    .disabled(model.progressLabel != nil)
    .allowsHitTesting(model.progressLabel == nil)
    .accessibilityHidden(model.progressLabel != nil)
    .overlay {
      if let progress = model.progressLabel {
        OperationProgress(label: progress)
      }
    }
    .background(Color(nsColor: .windowBackgroundColor))
    .background(MenuOpenObserver { Task { await model.menuOpened() } })
  }

  private func currentAccount(_ account: Account) -> some View {
    let usage = UsagePresentation(account.usage)
    return VStack(alignment: .leading, spacing: 0) {
      sectionTitle("CURRENT ACCOUNT")
      HStack(alignment: .center, spacing: 8) {
        Text(account.alias).font(.system(size: 21, weight: .medium))
          .lineLimit(1).help(account.alias)
        if let plan = usage.plan {
          Text(plan).font(.system(size: 11))
            .foregroundStyle(.secondary)
            .padding(.horizontal, 6).frame(height: 18)
            .background(.quaternary, in: RoundedRectangle(cornerRadius: 5))
            .fixedSize()
        }
        Spacer(minLength: 0)
        Image(systemName: "checkmark.circle")
          .font(.system(size: 15)).foregroundStyle(Color.accentColor)
          .accessibilityLabel("Observed active account")
      }
      .padding(.top, 8)
      HStack(alignment: .top, spacing: 24) {
        QuotaMeter(quota: usage.primary)
        QuotaMeter(quota: usage.secondary)
      }
      .padding(.top, 18)
    }
    .padding(.horizontal, 18).padding(.bottom, 20)
  }

  @ViewBuilder
  private func savedAccounts(_ status: UIStatus) -> some View {
    let accounts = status.accounts.filter { $0.id != status.activeAccountID }
    if status.accounts.isEmpty {
      noticeText("No accounts saved. Add an account with the CLI.")
        .padding(.horizontal, 18).padding(.bottom, 18)
    } else if !accounts.isEmpty {
      if activeAccount != nil { Divider() }
      VStack(alignment: .leading, spacing: 5) {
        sectionTitle(activeAccount == nil ? "SAVED ACCOUNTS" : "SWITCH ACCOUNT")
          .padding(.horizontal, 10)
        if accounts.count > 3 {
          // Long lists need an explicit viewport so MenuBarExtra cannot
          // collapse the scroll area during intrinsic size measurement.
          ScrollView {
            accountRows(accounts, canSwitch: status.canSwitch)
          }
          .frame(height: 180)
        } else {
          accountRows(accounts, canSwitch: status.canSwitch)
        }
      }
      .padding(.horizontal, 8).padding(.top, 14).padding(.bottom, 8)
    }
  }

  private func accountRows(_ accounts: [Account], canSwitch: Bool) -> some View {
    VStack(spacing: 0) {
      ForEach(accounts) { account in
        accountRow(account)
          .disabled(model.isBusy || !canSwitch)
          .opacity(canSwitch && !model.canCancel ? 1 : 0.5)
      }
    }
  }

  private func accountRow(_ account: Account) -> some View {
    let usage = UsagePresentation(account.usage)
    return Button {
      Task { await model.beginSwitch(accountID: account.id) }
    } label: {
      HStack(spacing: 10) {
        Text(String(account.alias.prefix(1)).uppercased())
          .font(.system(size: 11, weight: .medium)).foregroundStyle(.secondary)
          .frame(width: 28, height: 28)
          .background(.quaternary, in: RoundedRectangle(cornerRadius: 7))
        VStack(alignment: .leading, spacing: 3) {
          Text(account.alias).font(.system(size: 13, weight: .medium)).lineLimit(1)
          Text(usage.summary).font(.system(size: 11)).foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
          if account.usage != nil {
            Text(usage.cacheLabel).font(.system(size: 10))
              .foregroundStyle(
                account.usage?.isStaleOrUnavailable == false ? Color.secondary : .orange
              )
              .lineLimit(2)
          }
        }
        Spacer(minLength: 0)
        Image(systemName: "chevron.right").font(.system(size: 11))
          .foregroundStyle(.tertiary)
      }
      .frame(maxWidth: .infinity, alignment: .leading)
      .padding(.horizontal, 10).padding(.vertical, 8)
      .contentShape(Rectangle())
    }
    .buttonStyle(MenuRowStyle())
    .help("Switch to \(account.alias)")
  }

  private var footer: some View {
    HStack(spacing: 6) {
      Circle().fill(processStatusColor).frame(width: 5, height: 5)
        .accessibilityHidden(true)
      Text(processStatusLabel).foregroundStyle(.secondary)
      Spacer()
      Button("Quit") { NSApplication.shared.terminate(nil) }
        .buttonStyle(.plain).foregroundStyle(.secondary)
        .disabled(model.isBusy).help("Quit Codex Switch")
        .accessibilityLabel("Quit Codex Switch")
    }
    .font(.system(size: 11)).padding(.horizontal, 18).padding(.vertical, 11)
  }

  private var processStatusLabel: String {
    guard let status = model.status else { return "Process status unavailable" }
    if model.errorMessage != nil { return "Process status from last check" }
    return status.codexProcesses.isEmpty ? "Codex is closed" : "Codex is running"
  }

  private var processStatusColor: Color {
    guard let status = model.status, model.errorMessage == nil else { return .secondary }
    return status.codexProcesses.isEmpty ? .secondary : .green
  }

  private func sectionTitle(_ title: String) -> some View {
    Text(title).font(.system(size: 10, weight: .medium))
      .tracking(0.8).foregroundStyle(.secondary)
  }

  private func noticeText(_ text: String) -> some View {
    Text(text).font(.system(size: 11)).foregroundStyle(.secondary)
      .fixedSize(horizontal: false, vertical: true)
  }

  @ViewBuilder
  private var switchConfirmation: some View {
    if model.canCancel {
      VStack(alignment: .leading, spacing: 8) {
        Text("Switch to \(model.pendingAlias ?? "selected account")")
          .font(.system(size: 12, weight: .semibold))
        if let processes = model.status?.codexProcesses {
          ForEach(processes, id: \.self) { process in
            Text("\(process.command) · PID \(process.pid)")
              .font(.system(size: 10)).foregroundStyle(.secondary)
          }
        }
        switch model.phase {
        case .awaitingQuit:
          noticeText("Save your work. Quit Codex before switching?")
        case .awaitingForceQuit:
          Text(
            "Codex has not quit. Force Quit may lose unsaved work. Confirm separately to continue."
          )
          .font(.system(size: 11)).foregroundStyle(.orange)
        case .awaitingManualExit:
          noticeText(
            "A Codex CLI or unverified application is still running. Close it yourself, then Retry. No terminal processes will be terminated."
          )
        default: EmptyView()
        }
        HStack(spacing: 8) {
          Spacer(minLength: 0)
          Button("Cancel Switch", role: .cancel) { model.cancelSwitch() }
            .buttonStyle(ConfirmationButtonStyle())
          switch model.phase {
          case .awaitingQuit:
            Button("Quit Codex & Switch") { Task { await model.confirmQuit() } }
              .buttonStyle(ConfirmationButtonStyle(prominent: true))
          case .awaitingForceQuit:
            Button("Force Quit Codex", role: .destructive) {
              Task { await model.confirmForceQuit() }
            }
            .buttonStyle(ConfirmationButtonStyle(prominent: true, destructive: true))
          case .awaitingManualExit:
            Button("Retry Switch") { Task { await model.retrySwitch() } }
              .buttonStyle(ConfirmationButtonStyle(prominent: true))
          default: EmptyView()
          }
        }
        .padding(.top, 4)
      }
      .fixedSize(horizontal: false, vertical: true)
      .controlSize(.small)
      .padding(12).frame(maxWidth: .infinity, alignment: .leading)
      .background(.quaternary, in: RoundedRectangle(cornerRadius: 8))
      .padding(.horizontal, 12).padding(.bottom, 12)
    }
  }
}

private struct OperationProgress: View {
  let label: String
  @State private var showsProgress = false

  var body: some View {
    ZStack {
      Rectangle()
        .fill(Color(nsColor: .windowBackgroundColor).opacity(showsProgress ? 0.9 : 0))
      if showsProgress {
        VStack(spacing: 10) {
          ProgressView().controlSize(.small)
          Text(label).font(.system(size: 12, weight: .medium))
            .multilineTextAlignment(.center)
        }
        .padding(18)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 10))
        .padding(18)
      }
    }
    .contentShape(Rectangle())
    .onTapGesture {}  // Consume pointer input while the underlying menu is locked.
    .accessibilityElement(children: .ignore)
    .accessibilityLabel(label)
    .task {
      // Fast cached reads should not flash a spinner or resize the popover.
      do {
        try await Task.sleep(for: .milliseconds(150))
        showsProgress = true
      } catch {}
    }
  }
}

private struct QuotaMeter: View {
  let quota: QuotaPresentation

  var body: some View {
    VStack(alignment: .leading, spacing: 7) {
      HStack(alignment: .firstTextBaseline, spacing: 4) {
        Text(quota.title).font(.system(size: 11)).foregroundStyle(.secondary)
        Spacer(minLength: 0)
        if let remaining = quota.remaining {
          HStack(alignment: .firstTextBaseline, spacing: 2) {
            Text("\(remaining)").font(.system(size: 18, weight: .medium)).monospacedDigit()
            Text("%").font(.system(size: 11)).foregroundStyle(.secondary)
          }
        } else {
          Text("—").font(.system(size: 18)).foregroundStyle(.secondary)
        }
      }
      GeometryReader { geometry in
        Capsule().fill(.quaternary)
          .overlay(alignment: .leading) {
            Capsule().fill(Color.accentColor.opacity(0.75))
              .frame(width: geometry.size.width * Double(quota.remaining ?? 0) / 100)
          }
      }
      .frame(height: 3)
      Text(quota.remaining == nil ? "unavailable" : "remaining")
        .font(.system(size: 11)).foregroundStyle(.secondary)
    }
    .frame(maxWidth: .infinity)
    .accessibilityElement(children: .ignore)
    .accessibilityLabel(quota.accessibilityLabel)
  }
}

private struct MenuAction: View {
  let title: String
  let symbol: String
  var opensApplication = false
  let action: () -> Void

  init(
    _ title: String, symbol: String, opensApplication: Bool = false, action: @escaping () -> Void
  ) {
    self.title = title
    self.symbol = symbol
    self.opensApplication = opensApplication
    self.action = action
  }

  var body: some View {
    Button(action: action) {
      HStack(spacing: 9) {
        Image(systemName: symbol).font(.system(size: 14)).foregroundStyle(.secondary)
          .frame(width: 16)
        Text(title).font(.system(size: 12))
        Spacer()
        if opensApplication {
          Image(systemName: "arrow.up.right").font(.system(size: 10)).foregroundStyle(.tertiary)
        }
      }
      .padding(.horizontal, 10).padding(.vertical, 9).contentShape(Rectangle())
    }
    .buttonStyle(MenuRowStyle())
  }
}

private struct ConfirmationButtonStyle: ButtonStyle {
  var prominent = false
  var destructive = false

  func makeBody(configuration: Configuration) -> some View {
    configuration.label
      .font(.system(size: 11, weight: .medium))
      .foregroundStyle(
        prominent ? (destructive ? Color.white : Color(nsColor: .windowBackgroundColor)) : .primary
      )
      .padding(.horizontal, 10).padding(.vertical, 7)
      .background(
        prominent
          ? (destructive ? Color.red : Color.primary) : Color(nsColor: .controlBackgroundColor),
        in: RoundedRectangle(cornerRadius: 6)
      )
      .overlay {
        RoundedRectangle(cornerRadius: 6).strokeBorder(Color.primary.opacity(0.08), lineWidth: 0.5)
      }
      .opacity(configuration.isPressed ? 0.75 : 1)
  }
}

private struct MenuRowStyle: ButtonStyle {
  func makeBody(configuration: Configuration) -> some View {
    HoverRow(content: configuration.label, isPressed: configuration.isPressed)
  }

  private struct HoverRow<Content: View>: View {
    let content: Content
    let isPressed: Bool
    @Environment(\.isEnabled) private var isEnabled
    @State private var isHovered = false

    var body: some View {
      content
        .background(
          Color.primary.opacity(isEnabled && (isHovered || isPressed) ? 0.06 : 0),
          in: RoundedRectangle(cornerRadius: 7)
        )
        .onHover { isHovered = $0 }
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
