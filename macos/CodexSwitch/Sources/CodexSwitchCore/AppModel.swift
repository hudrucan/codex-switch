import Combine
import Foundation

@MainActor
public final class AppModel: ObservableObject {
  public enum Phase: Equatable {
    case idle, loading, refreshing, checking, switching, opening
    case awaitingQuit, quitting, awaitingForceQuit, forceQuitting, awaitingManualExit
  }

  @Published public private(set) var status: UIStatus?
  @Published public private(set) var phase: Phase = .idle
  @Published public private(set) var errorMessage: String?
  @Published public private(set) var pendingAlias: String?
  @Published public private(set) var refreshNotice: String?

  private let client: any CLIClientProtocol
  private let applications: any ApplicationControlling
  private let quitPollAttempts: Int
  private let wait: @Sendable () async throws -> Void
  private let now: () -> Date
  private var menuIsOpen = false
  private var nextRefreshAt: Date?
  private var refreshFailures = 0
  private var pendingID: String?
  private var targets: [ApplicationTarget] = []

  public init(
    client: any CLIClientProtocol, applications: any ApplicationControlling,
    quitPollAttempts: Int = 12,
    now: @escaping () -> Date = { Date() },
    wait: @escaping @Sendable () async throws -> Void = {
      try await Task.sleep(for: .milliseconds(500))
    }
  ) {
    self.client = client
    self.applications = applications
    self.quitPollAttempts = max(1, quitPollAttempts)
    self.wait = wait
    self.now = now
  }

  public var isBusy: Bool { phase != .idle }
  public var canCancel: Bool {
    [.awaitingQuit, .awaitingForceQuit, .awaitingManualExit].contains(phase)
  }

  public var progressLabel: String? {
    switch phase {
    case .loading, .checking: return "Reading cached status…"
    case .refreshing: return "Refreshing usage…"
    case .switching: return "Switching account…"
    case .quitting: return "Waiting for Codex to quit…"
    case .forceQuitting: return "Verifying Force Quit…"
    case .opening: return "Opening Codex…"
    default: return nil
    }
  }

  public func menuOpened() async {
    guard !Task.isCancelled else { return }
    menuIsOpen = true
    guard !isBusy else { return }
    phase = .loading
    do {
      status = try await client.status()
      errorMessage = nil
    } catch {
      show(error, fallback: "Cached status could not be read. Previous menu data is retained.")
      phase = .idle
      return
    }
    phase = .idle
    guard menuIsOpen, !Task.isCancelled, let status,
      status.accounts.contains(where: { account in
        guard let fetched = account.usage?.fetchedDate else { return true }
        return now().timeIntervalSince(fetched) > 5 * 60 || fetched > now()
      })
    else { return }
    await refreshUsage()
  }

  public func menuClosed() { menuIsOpen = false }

  public func refreshUsage() async {
    guard !isBusy else { return }
    if let nextRefreshAt, now() < nextRefreshAt {
      let formatter = DateFormatter()
      formatter.locale = Locale(identifier: "en_US_POSIX")
      formatter.dateFormat = "h:mm:ss a"
      refreshNotice = "Refresh available after \(formatter.string(from: nextRefreshAt))."
      return
    }
    refreshNotice = nil
    nextRefreshAt = now().addingTimeInterval(60)
    phase = .refreshing
    defer { phase = .idle }
    do {
      try await client.refreshUsage()
      let updated = try await client.status()
      status = updated
      errorMessage = nil
      refreshFailures = 0
      nextRefreshAt = now().addingTimeInterval(60)
    } catch {
      refreshFailures = min(refreshFailures + 1, 3)
      let delay: TimeInterval = refreshFailures == 1 ? 60 : (refreshFailures == 2 ? 120 : 300)
      nextRefreshAt = now().addingTimeInterval(delay)
      show(error, fallback: "Usage refresh failed. Previous menu data is retained.")
      let minutes = Int(delay / 60)
      refreshNotice = "Try refreshing again in \(minutes) \(minutes == 1 ? "minute" : "minutes")."
    }
  }

  public func beginSwitch(accountID: String) async {
    guard !isBusy else { return }
    pendingID = accountID
    errorMessage = nil
    phase = .checking
    do {
      let latest = try await readStatus()
      try await routeSwitch(latest)
    } catch { finishWithError(error) }
  }

  public func retrySwitch() async {
    guard phase == .awaitingManualExit else { return }
    phase = .checking
    do { try await routeSwitch(readStatus()) } catch { finishWithError(error) }
  }

  public func cancelSwitch() {
    guard canCancel else { return }
    clearPending()
    phase = .idle
    errorMessage = nil
  }

  public func confirmQuit() async {
    guard phase == .awaitingQuit else { return }
    phase = .quitting
    await terminateConfirmed(force: false)
  }

  public func confirmForceQuit() async {
    guard phase == .awaitingForceQuit else { return }
    phase = .forceQuitting
    await terminateConfirmed(force: true)
  }

  public func openCodex() async {
    guard !isBusy else { return }
    phase = .opening
    defer { phase = .idle }
    do {
      try await applications.openCodex()
    } catch {
      show(error, fallback: "Codex could not be opened.")
      return
    }
    do {
      // Opening an app changes process state without reopening the menu.
      status = try await client.status()
      errorMessage = nil
    } catch { show(error, fallback: "Codex opened, but cached status could not be reloaded.") }
  }

  private func readStatus() async throws -> UIStatus {
    let latest = try await client.status()
    status = latest
    return latest
  }

  private func routeSwitch(_ latest: UIStatus) async throws {
    guard latest.canSwitch, let account = latest.accounts.first(where: { $0.id == pendingID })
    else {
      errorMessage = "This account state cannot be switched from the menu. Review it with the CLI."
      clearPending()
      phase = .idle
      return
    }
    try CLIClient.validateAlias(account.alias)
    guard !latest.accounts.contains(where: { $0.id != account.id && $0.id == account.alias }) else {
      throw UIError.ambiguousAlias
    }
    pendingAlias = account.alias
    if latest.activeAccountID == account.id {
      clearPending()
      phase = .idle
    } else if latest.codexProcesses.isEmpty {
      await guardedSwitch(alias: account.alias)
    } else {
      prepareQuit(latest)
    }
  }

  private func prepareQuit(_ latest: UIStatus) {
    let identified = latest.codexProcesses.compactMap { applications.identify($0) }
    if identified.count == latest.codexProcesses.count {
      var seen: Set<ApplicationTarget> = []
      targets = identified.filter { seen.insert($0).inserted }
      phase = .awaitingQuit
    } else {
      targets = []
      phase = .awaitingManualExit
    }
  }

  private func terminateConfirmed(force: Bool) async {
    let consented = targets
    var observedProcesses: Set<CodexProcess> = []
    var guiStillRunning = true
    do {
      for target in consented {
        // Read immediately before EACH termination, never from the consent snapshot.
        let latest = try await readStatus()
        guard latest.canSwitch, latest.activeAccountID != pendingID,
          latest.accounts.contains(where: { $0.id == pendingID && $0.alias == pendingAlias }),
          !latest.accounts.contains(where: { $0.id != pendingID && $0.id == pendingAlias })
        else {
          try await routeSwitch(latest)
          return
        }
        let current = latest.codexProcesses.compactMap { applications.identify($0) }
        guard current.count == latest.codexProcesses.count,
          current.allSatisfy({ consented.contains($0) })
        else {
          // A new process or reused PID needs fresh consent, including after Force Quit.
          errorMessage = "Running processes changed. Review them before continuing."
          prepareQuit(latest)
          return
        }
        observedProcesses.formUnion(latest.codexProcesses)
        if current.contains(target) {
          _ = applications.requestTermination(target, force: force)
        }
      }
      for _ in 0..<quitPollAttempts {
        let latest = try await readStatus()
        guard latest.canSwitch else {
          try await routeSwitch(latest)
          return
        }
        if latest.codexProcesses.isEmpty {
          try await routeSwitch(latest)
          return
        }
        let current = latest.codexProcesses.compactMap { applications.identify($0) }
        guard current.allSatisfy({ consented.contains($0) }) else {
          prepareQuit(latest)
          return
        }
        guiStillRunning = current.count == latest.codexProcesses.count
        if guiStillRunning {
          observedProcesses.formUnion(latest.codexProcesses)
        } else {
          guard latest.codexProcesses.allSatisfy({ observedProcesses.contains($0) }) else {
            prepareQuit(latest)
            return
          }
          // Previously observed children can briefly outlive their GUI parent.
          // Only wait: never signal an orphan or switch while the CLI sees it.
        }
        try await wait()
      }
      // A second explicit confirmation is required; never escalate automatically.
      phase = force || !guiStillRunning ? .awaitingManualExit : .awaitingForceQuit
    } catch { finishWithError(error) }
  }

  private func guardedSwitch(alias: String) async {
    phase = .switching
    do {
      try await client.use(alias: alias)
      // Keep the operation locked until its final cached status is available.
      // A read failure must not retry the already successful switch.
      do {
        status = try await client.status()
        errorMessage = nil
      } catch {
        show(error, fallback: "Account switched, but cached status could not be reloaded.")
      }
      clearPending()
      phase = .idle
    } catch {
      show(error, fallback: "The guarded switch failed. No process bypass was attempted.")
      // Error output is not a machine-readable process API. Re-observe instead of parsing it.
      do {
        let latest = try await readStatus()
        if latest.canSwitch && !latest.codexProcesses.isEmpty {
          prepareQuit(latest)
          return
        }
      } catch {
        // Retain the last successful status and the safe switch error.
      }
      clearPending()
      phase = .idle
    }
  }

  private func clearPending() {
    pendingID = nil
    pendingAlias = nil
    targets = []
  }

  private func finishWithError(_ error: any Error) {
    show(
      error,
      fallback: "Status could not be verified. Switching stopped; retry after checking Codex.")
    clearPending()
    phase = .idle
  }

  private func show(_ error: any Error, fallback: String) {
    errorMessage = (error as? UIError)?.errorDescription ?? fallback
  }
}
