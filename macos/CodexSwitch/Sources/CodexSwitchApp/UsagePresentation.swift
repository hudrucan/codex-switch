import CodexSwitchCore
import Foundation

// Presentation only: the CLI remains the owner of quota and cache state.
struct UsagePresentation {
  let plan: String?
  let primary: QuotaPresentation
  let secondary: QuotaPresentation
  let cacheLabel: String
  let bankedResetCount: Int64?

  var bankedResetLabel: String {
    bankedResetCount.map { "\($0) available" } ?? "Unavailable"
  }

  init(_ usage: CachedUsage?) {
    let available = usage?.status == "fresh" || usage?.status == "stale"
    plan = available ? usage?.planType?.capitalized : nil
    primary = QuotaPresentation(
      window: available ? usage?.rateLimits?.main.primary : nil, fallback: "Primary")
    secondary = QuotaPresentation(
      window: available ? usage?.rateLimits?.main.secondary : nil, fallback: "Secondary")
    let reportedCount = available ? usage?.rateLimits?.rateLimitResetCredits?.availableCount : nil
    bankedResetCount = reportedCount.flatMap { $0 >= 0 ? $0 : nil }

    var labels = [
      available ? (usage?.status == "stale" ? "Stale cache" : "Cached") : "Usage unavailable"
    ]
    if let updated = Self.cachedUpdateTime(usage?.fetchedAt) {
      labels.append(updated)
    } else if available {
      labels.append("No cached update time")
    }
    if usage?.partial?.isEmpty == false { labels.append("Partial") }
    if usage?.error?.isEmpty == false { labels.append("Refresh failed") }
    cacheLabel = labels.joined(separator: " · ")
  }

  private static func cachedUpdateTime(_ fetchedAt: String?) -> String? {
    guard let fetchedAt else { return nil }
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let fractional = formatter.date(from: fetchedAt)
    formatter.formatOptions = [.withInternetDateTime]
    guard let date = fractional ?? formatter.date(from: fetchedAt), date.timeIntervalSince1970 > 0
    else { return nil }
    return date.formatted(date: .abbreviated, time: .shortened)
  }
}

struct QuotaPresentation {
  let title: String
  let remaining: Int?
  let resetAt: Date?

  var resetLabel: String {
    guard let resetAt else { return "Reset time unavailable" }
    return "Resets " + resetAt.formatted(date: .abbreviated, time: .shortened)
  }

  init(window: QuotaWindow?, fallback: String) {
    switch window?.windowDurationMins {
    case 300: title = "5-hour"
    case 10080: title = "Weekly"
    case let minutes? where minutes > 0: title = "\(minutes)m"
    default: title = fallback
    }
    remaining = window.map { 100 - min(100, max(0, $0.usedPercent)) }
    // Reject missing, nonpositive and out-of-calendar-range Unix timestamps.
    if let seconds = window?.resetsAt, seconds > 0, seconds <= 253_402_300_799 {
      resetAt = Date(timeIntervalSince1970: Double(seconds))
    } else {
      resetAt = nil
    }
  }

  var accessibilityLabel: String {
    if let remaining { return "\(title), \(remaining)% remaining, cached. \(resetLabel)" }
    return "\(title), usage unavailable"
  }
}
