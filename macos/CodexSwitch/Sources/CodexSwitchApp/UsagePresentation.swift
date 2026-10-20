import CodexSwitchCore
import Foundation

// Presentation only: the CLI remains the owner of quota and cache state.
struct UsagePresentation {
  let plan: String?
  let primary: QuotaPresentation
  let secondary: QuotaPresentation
  let cacheLabel: String
  let compactCacheLabel: String
  let bankedResetCount: Int64?

  var bankedResetLabel: String {
    bankedResetCount.map { "\($0) available" } ?? "Unavailable"
  }

  init(_ usage: CachedUsage?, now: Date = Date(), calendar: Calendar = .current) {
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
    var compactLabels = labels
    if let updated = Self.cachedUpdateTime(usage?.fetchedAt) {
      labels.append(CompactUsageDate.fullLabel(for: updated, calendar: calendar))
      compactLabels.append(
        "updated "
          + CompactUsageDate.label(
            for: updated, relativeTo: now, calendar: calendar,
            omitToday: true))
    } else if available {
      labels.append("No cached update time")
      compactLabels.append("No cached update time")
    }
    if usage?.partial?.isEmpty == false { labels.append("Partial") }
    if usage?.error?.isEmpty == false { labels.append("Refresh failed") }
    cacheLabel = labels.joined(separator: " · ")
    if usage?.partial?.isEmpty == false { compactLabels.append("Partial") }
    if usage?.error?.isEmpty == false { compactLabels.append("Refresh failed") }
    compactCacheLabel = compactLabels.joined(separator: " · ")
  }

  private static func cachedUpdateTime(_ fetchedAt: String?) -> Date? {
    guard let fetchedAt else { return nil }
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let fractional = formatter.date(from: fetchedAt)
    formatter.formatOptions = [.withInternetDateTime]
    guard let date = fractional ?? formatter.date(from: fetchedAt), date.timeIntervalSince1970 > 0
    else { return nil }
    return date
  }
}

struct QuotaPresentation {
  let title: String
  let remaining: Int?
  let resetAt: Date?

  var resetLabel: String {
    guard let resetAt else { return "Reset time unavailable" }
    return "Resets " + CompactUsageDate.fullLabel(for: resetAt)
  }

  func compactResetLabel(now: Date = Date(), calendar: Calendar = .current) -> String {
    guard let resetAt else { return "reset unavailable" }
    return "resets " + CompactUsageDate.label(for: resetAt, relativeTo: now, calendar: calendar)
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

private enum CompactUsageDate {
  static func fullLabel(for date: Date, calendar: Calendar = .current) -> String {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_US_POSIX")
    formatter.calendar = calendar
    formatter.timeZone = calendar.timeZone
    formatter.dateFormat = "d MMM yyyy 'at' h:mm a"
    return formatter.string(from: date)
  }

  static func label(
    for date: Date, relativeTo now: Date, calendar: Calendar, omitToday: Bool = false
  ) -> String {
    let sameDay = calendar.isDate(date, inSameDayAs: now)
    let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_US_POSIX")
    formatter.calendar = calendar
    formatter.timeZone = calendar.timeZone
    formatter.dateFormat = sameDay ? "h:mm a" : (sameYear ? "d MMM h:mm a" : "d MMM yyyy h:mm a")
    let formatted = formatter.string(from: date)
    return sameDay && !omitToday ? "today " + formatted : formatted
  }
}
