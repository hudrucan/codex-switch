import AppKit
import CodexSwitchCore
import SwiftUI

private enum MenuPalette {
  static let accent = Color(.sRGB, red: 0.16, green: 0.42, blue: 0.84, opacity: 1)
}

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
    MenuBarExtra {
      SwitchMenu(model: model)
    } label: {
      Image(nsImage: MenuBarIcon.image).accessibilityLabel("Codex Switch")
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
  @State private var showsProcessDetails = false

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
        MenuAction(
          "Refresh Usage", symbol: "arrow.clockwise",
          progress: model.phase == .refreshing ? model.progressLabel : nil
        ) {
          Task { await model.refreshUsage() }
        }
        refreshFeedback
        if let notice = model.refreshNotice {
          Text(notice).font(.system(size: 10)).foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10).padding(.bottom, 6)
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
    .frame(width: 400)
    .disabled(model.progressLabel != nil)
    .background(Color(nsColor: .windowBackgroundColor))
    .background(
      MenuOpenObserver(
        opened: { await model.menuOpened() }, closed: { model.menuClosed() })
    )
    .onChange(of: model.canCancel) { awaitingConfirmation in
      if !awaitingConfirmation { showsProcessDetails = false }
    }
  }

  private var refreshFeedback: some View {
    VStack(alignment: .leading, spacing: 3) {
      if let startedAt = model.refreshStartedAt {
        HStack(spacing: 4) {
          if model.isReloadingUsage {
            Text("Updating menu results")
          } else if let count = model.status?.accounts.count {
            Text("Refreshing \(count) \(count == 1 ? "account" : "accounts")")
          } else {
            Text("Refreshing saved accounts")
          }
          Text("·")
          Text(startedAt, style: .timer).monospacedDigit().fixedSize()
          Spacer(minLength: 0)
        }
        Text("Showing cached usage. You can close this menu.")
      } else {
        Text("Usage is cached. Refresh updates all saved accounts.")
        Text("Multiple accounts can take longer to refresh.")
      }
    }
    .font(.system(size: 10)).foregroundStyle(.secondary)
    .frame(maxWidth: .infinity, minHeight: 32, maxHeight: 32, alignment: .leading)
    .padding(.horizontal, 10).padding(.bottom, 6)
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
          .font(.system(size: 15)).foregroundStyle(MenuPalette.accent)
          .accessibilityLabel("Observed active account")
      }
      .padding(.top, 8)
      HStack(alignment: .top, spacing: 24) {
        QuotaMeter(quota: usage.primary)
        QuotaMeter(quota: usage.secondary)
      }
      .padding(.top, 18)
      HStack(alignment: .firstTextBaseline, spacing: 8) {
        Text("Banked resets").foregroundStyle(.secondary)
        Text(usage.bankedResetLabel).fontWeight(.medium).monospacedDigit()
      }
      .font(.system(size: 11)).padding(.top, 10)
      .help("Cached earned manual rate-limit resets, separate from scheduled quota resets.")
      Text(usage.compactCacheLabel).font(.system(size: 10))
        .foregroundStyle(account.usage?.isStaleOrUnavailable == false ? Color.secondary : .orange)
        .fixedSize(horizontal: false, vertical: true)
        .help(usage.cacheLabel).padding(.top, 10)
    }
    .padding(.horizontal, 18).padding(.bottom, 16)
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
        if accounts.count > 1 {
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
      VStack(alignment: .leading, spacing: 8) {
        HStack(spacing: 10) {
          Text(String(account.alias.prefix(1)).uppercased())
            .font(.system(size: 11, weight: .medium)).foregroundStyle(.secondary)
            .frame(width: 28, height: 28)
            .background(.quaternary, in: RoundedRectangle(cornerRadius: 7))
          HStack(alignment: .firstTextBaseline, spacing: 6) {
            Text(account.alias).font(.system(size: 13, weight: .medium)).lineLimit(1)
            if let plan = usage.plan {
              Text(plan).font(.system(size: 10)).foregroundStyle(.secondary)
            }
          }
          Spacer(minLength: 0)
          Image(systemName: "chevron.right").font(.system(size: 11))
            .foregroundStyle(.tertiary)
        }
        VStack(alignment: .leading, spacing: 5) {
          SavedQuotaRow(quota: usage.primary)
          SavedQuotaRow(quota: usage.secondary)
          Text("Banked resets · \(usage.bankedResetCount.map(String.init) ?? "Unavailable")")
            .foregroundStyle(.secondary)
          Text(usage.compactCacheLabel)
            .foregroundStyle(
              account.usage?.isStaleOrUnavailable == false ? Color.secondary : .orange
            )
            .help(usage.cacheLabel)
        }
        .font(.system(size: 11))
        .fixedSize(horizontal: false, vertical: true)
      }
      .frame(maxWidth: .infinity, alignment: .leading)
      .padding(.horizontal, 10).padding(.vertical, 10)
      .contentShape(Rectangle())
    }
    .buttonStyle(MenuRowStyle())
    .help("Switch to \(account.alias)")
  }

  private var footer: some View {
    HStack(spacing: 6) {
      Group {
        if footerProgress != nil {
          ProgressView().controlSize(.mini)
        } else {
          Circle().fill(processStatusColor).frame(width: 5, height: 5)
        }
      }
      .frame(width: 12, height: 14).accessibilityHidden(true)
      Text(footerProgress ?? processStatusLabel)
        .foregroundStyle(.secondary).lineLimit(1)
      Spacer()
      Button("Quit") { NSApplication.shared.terminate(nil) }
        .buttonStyle(.plain).foregroundStyle(.secondary)
        .disabled(model.isBusy).help("Quit Codex Switch")
        .accessibilityLabel("Quit Codex Switch")
    }
    .font(.system(size: 11)).padding(.horizontal, 18).padding(.vertical, 11)
  }

  private var footerProgress: String? {
    model.phase == .refreshing ? nil : model.progressLabel
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
        if let processes = model.status?.codexProcesses, !processes.isEmpty {
          DisclosureGroup(
            "\(processes.count) running processes", isExpanded: $showsProcessDetails
          ) {
            ScrollView {
              VStack(alignment: .leading, spacing: 3) {
                ForEach(processes, id: \.self) { process in
                  Text("\(process.command) · PID \(process.pid)")
                    .font(.system(size: 10)).lineLimit(1)
                }
              }
              .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(height: min(CGFloat(processes.count) * 16, 96))
          }
          .font(.system(size: 10)).foregroundStyle(.secondary)
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

private struct SavedQuotaRow: View {
  let quota: QuotaPresentation

  var body: some View {
    Text(
      "\(quota.title) · \(quota.remaining.map { "\($0)% left" } ?? "Unavailable") · \(quota.compactResetLabel())"
    )
    .foregroundStyle(.secondary)
    .help(quota.resetLabel)
    .accessibilityElement(children: .ignore)
    .accessibilityLabel(quota.accessibilityLabel)
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
            Capsule().fill(MenuPalette.accent)
              .frame(width: geometry.size.width * Double(quota.remaining ?? 0) / 100)
          }
      }
      .frame(height: 3)
      if quota.remaining == nil {
        Text("unavailable").font(.system(size: 11)).foregroundStyle(.secondary)
      }
      Text(quota.resetLabel).font(.system(size: 10)).foregroundStyle(.secondary)
        .lineLimit(2).fixedSize(horizontal: false, vertical: true)
        .help(quota.resetLabel)
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
  let progress: String?
  let action: () -> Void

  init(
    _ title: String, symbol: String, opensApplication: Bool = false,
    progress: String? = nil, action: @escaping () -> Void
  ) {
    self.title = title
    self.symbol = symbol
    self.opensApplication = opensApplication
    self.progress = progress
    self.action = action
  }

  var body: some View {
    Button(action: action) {
      HStack(spacing: 9) {
        Group {
          if progress != nil {
            ProgressView().controlSize(.mini)
          } else {
            Image(systemName: symbol).font(.system(size: 14)).foregroundStyle(.secondary)
          }
        }
        .frame(width: 16, height: 16).accessibilityHidden(true)
        Text(progress ?? title).font(.system(size: 12)).lineLimit(1)
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
  @Environment(\.isEnabled) private var isEnabled

  func makeBody(configuration: Configuration) -> some View {
    configuration.label
      .font(.system(size: 11, weight: .semibold))
      .foregroundStyle(prominent ? Color.white : .primary)
      .padding(.horizontal, 10).padding(.vertical, 7)
      .background(
        prominent
          ? (destructive ? Color.red : MenuPalette.accent)
          : Color(nsColor: .controlBackgroundColor),
        in: RoundedRectangle(cornerRadius: 6)
      )
      .overlay {
        RoundedRectangle(cornerRadius: 6).strokeBorder(Color.primary.opacity(0.08), lineWidth: 0.5)
      }
      .opacity(isEnabled ? (configuration.isPressed ? 0.8 : 1) : 0.45)
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
  let opened: @MainActor () async -> Void
  let closed: @MainActor () -> Void

  func makeNSView(context: Context) -> OpenView { OpenView(opened: opened, closed: closed) }
  func updateNSView(_ view: OpenView, context: Context) {
    view.opened = opened
    view.closed = closed
  }

  static func dismantleNSView(_ view: OpenView, coordinator: ()) { view.windowClosed() }

  final class OpenView: NSView {
    var opened: @MainActor () async -> Void
    var closed: @MainActor () -> Void

    private var openTask: Task<Void, Never>?

    init(opened: @escaping @MainActor () async -> Void, closed: @escaping @MainActor () -> Void) {
      self.opened = opened
      self.closed = closed
      super.init(frame: .zero)
    }

    required init?(coder: NSCoder) { nil }

    override func viewDidMoveToWindow() {
      super.viewDidMoveToWindow()
      NotificationCenter.default.removeObserver(self)
      windowClosed()
      guard let window else { return }
      NotificationCenter.default.addObserver(
        self, selector: #selector(windowOpened), name: NSWindow.didBecomeKeyNotification,
        object: window
      )
      for name in [NSWindow.didResignKeyNotification, NSWindow.willCloseNotification] {
        NotificationCenter.default.addObserver(
          self, selector: #selector(windowClosed), name: name, object: window)
      }
      if window.isVisible { windowOpened() }
    }

    @objc private func windowOpened() {
      openTask?.cancel()
      openTask = Task { await opened() }
    }
    @objc func windowClosed() {
      openTask?.cancel()
      closed()
    }

    deinit { NotificationCenter.default.removeObserver(self) }
  }
}
