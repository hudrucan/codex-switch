import CodexSwitchCore

// Presentation only: the CLI remains the owner of quota and cache state.
struct UsagePresentation {
  let plan: String?
  let primary: QuotaPresentation
  let secondary: QuotaPresentation
  let summary: String
  let cacheLabel: String

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
    summary = ([plan].compactMap { $0 } + (quotas.isEmpty ? ["Usage unavailable"] : quotas))
      .joined(separator: " · ")

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

  init(window: QuotaWindow?, fallback: String) {
    switch window?.windowDurationMins {
    case 300: title = "5-hour"
    case 10080: title = "Weekly"
    case let minutes? where minutes > 0: title = "\(minutes)m"
    default: title = fallback
    }
    remaining = window.map { 100 - min(100, max(0, $0.usedPercent)) }
  }

  var accessibilityLabel: String {
    if let remaining { return "\(title), \(remaining)% remaining, cached" }
    return "\(title), usage unavailable"
  }
}
