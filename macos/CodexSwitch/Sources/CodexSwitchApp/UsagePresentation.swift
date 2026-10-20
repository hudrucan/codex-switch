import CodexSwitchCore
import Foundation

// Presentation only: the CLI remains the owner of quota and cache state.
struct UsagePresentation {
  let plan: String?
  let primary: QuotaPresentation
  let secondary: QuotaPresentation
  let summary: String
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
    let quotas = [primary, secondary].compactMap { quota -> String? in
      guard let remaining = quota.remaining else { return nil }
      return "\(quota.title) \(remaining)% left"
    }
    let reportedCount = available ? usage?.rateLimits?.rateLimitResetCredits?.availableCount : nil
    bankedResetCount = reportedCount.flatMap { $0 >= 0 ? $0 : nil }
    let banked = bankedResetCount.map { "\($0) banked resets" }
    summary =
      ([plan].compactMap { $0 } + (quotas.isEmpty ? ["Usage unavailable"] : quotas)
      + [banked].compactMap { $0 }).joined(separator: " · ")

    var labels = [
      available ? (usage?.status == "stale" ? "Stale cache" : "Cached") : "Usage unavailable"
    ]
    if let updated = usage?.updatedLabel, updated.hasPrefix("Cached ") {
      labels.append(String(updated.dropFirst("Cached ".count)))
    } else if available {
      labels.append("No cached update time")
    }
    if usage?.partial?.isEmpty == false { labels.append("Partial") }
    if usage?.error?.isEmpty == false { labels.append("Refresh failed") }
    cacheLabel = labels.joined(separator: " · ")
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
