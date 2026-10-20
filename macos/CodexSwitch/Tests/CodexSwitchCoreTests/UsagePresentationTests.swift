import CodexSwitchCore
import Foundation
import Testing

@testable import CodexSwitchApp

struct UsagePresentationTests {
  @Test func cachedQuotasUseReportedWindowsAndRemainingPercent() throws {
    let status = try UIStatus.decode(statusData())
    let usage = UsagePresentation(status.accounts[0].usage)
    #expect(usage.plan == "Pro")
    #expect(usage.primary.title == "5-hour")
    #expect(usage.primary.remaining == 62)
    #expect(usage.secondary.title == "Weekly")
    #expect(usage.secondary.remaining == 41)
    #expect(usage.cacheLabel.hasPrefix("Stale cache · "))
    #expect(usage.primary.accessibilityLabel.hasPrefix("5-hour, 62% remaining, cached. Resets "))
    #expect(usage.bankedResetCount == 2)
  }

  @Test func unavailableAndUnknownCacheStatesDoNotDisplayOldQuota() throws {
    for state in ["unavailable", "future_state"] {
      let usage = try JSONDecoder().decode(
        CachedUsage.self,
        from: Data(
          """
          {"status":"\(state)","rateLimits":{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300,"resetsAt":1792483200}},"rateLimitResetCredits":{"availableCount":2}}}
          """.utf8))
      let presentation = UsagePresentation(usage)
      #expect(presentation.primary.remaining == nil)
      #expect(presentation.secondary.remaining == nil)
      #expect(presentation.cacheLabel.hasPrefix("Usage unavailable"))
      #expect(presentation.bankedResetCount == nil)
      #expect(presentation.primary.resetAt == nil)
    }
    let missing = UsagePresentation(nil)
    #expect(missing.primary.title == "Primary")
    #expect(missing.secondary.title == "Secondary")
    #expect(missing.primary.accessibilityLabel == "Primary, usage unavailable")
    #expect(missing.cacheLabel == "Usage unavailable")
  }

  @Test func partialAndFailedCacheKeepVisibleWarnings() throws {
    let usage = try JSONDecoder().decode(
      CachedUsage.self,
      from: Data(
        #"{"status":"fresh","partial":["secondary"],"error":"synthetic error"}"#.utf8))
    let presentation = UsagePresentation(usage)
    #expect(presentation.cacheLabel == "Cached · No cached update time · Partial · Refresh failed")
    #expect(presentation.primary.remaining == nil)
    #expect(presentation.secondary.remaining == nil)
  }

  @Test(arguments: ["2026-10-20T01:02:03Z", "2026-10-20T01:02:03.123456789Z"])
  func cachedUpdateTimesKeepLocalFormatting(timestamp: String) throws {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_US_POSIX")
    formatter.dateFormat = "d MMM yyyy 'at' h:mm a"
    let updated = formatter.string(from: Date(timeIntervalSince1970: 1_792_458_123))
    for (state, label) in [
      ("fresh", "Cached"), ("stale", "Stale cache"),
      ("unavailable", "Usage unavailable"), ("future_state", "Usage unavailable"),
    ] {
      let usage = try JSONDecoder().decode(
        CachedUsage.self,
        from: JSONSerialization.data(withJSONObject: ["status": state, "fetchedAt": timestamp]))
      #expect(UsagePresentation(usage).cacheLabel == "\(label) · \(updated)")
    }
  }

  @Test(arguments: [nil, "invalid", "1970-01-01T00:00:00Z", "1969-12-31T23:59:59Z"] as [String?])
  func missingOrInvalidCacheTimesKeepTheirWarnings(timestamp: String?) throws {
    for (state, label) in [
      ("fresh", "Cached · No cached update time"),
      ("stale", "Stale cache · No cached update time"),
      ("unavailable", "Usage unavailable"), ("future_state", "Usage unavailable"),
    ] {
      var payload = ["status": state]
      payload["fetchedAt"] = timestamp
      let usage = try JSONDecoder().decode(
        CachedUsage.self, from: JSONSerialization.data(withJSONObject: payload))
      #expect(UsagePresentation(usage).cacheLabel == label)
    }
  }

  @Test func quotaClampsExtremeValuesAndDoesNotInventWindowDuration() throws {
    for (used, remaining) in [(Int.min, 100), (Int.max, 0)] {
      let quota = try JSONDecoder().decode(
        QuotaWindow.self, from: Data("{\"usedPercent\":\(used),\"windowDurationMins\":60}".utf8))
      let presentation = QuotaPresentation(window: quota, fallback: "Primary")
      #expect(presentation.remaining == remaining)
      #expect(presentation.title == "60m")
    }
    let unknown = try JSONDecoder().decode(
      QuotaWindow.self, from: Data(#"{"usedPercent":20}"#.utf8))
    #expect(QuotaPresentation(window: unknown, fallback: "Primary").title == "Primary")
  }
  @Test func resetTimesUseCachedUnixTimestampsAndRejectInvalidValues() throws {
    for seconds: Int64 in [0, -1, Int64.max] {
      let window = try JSONDecoder().decode(
        QuotaWindow.self,
        from: Data("{\"usedPercent\":20,\"resetsAt\":\(seconds)}".utf8))
      let quota = QuotaPresentation(window: window, fallback: "Primary")
      #expect(quota.resetAt == nil)
      #expect(quota.resetLabel == "Reset time unavailable")
    }
    let status = try UIStatus.decode(statusData())
    let usage = UsagePresentation(status.accounts[0].usage)
    #expect(usage.primary.resetAt?.timeIntervalSince1970 == 1_792_483_200)
    #expect(usage.secondary.resetAt?.timeIntervalSince1970 == 1_792_742_400)
    #expect(usage.primary.resetLabel.hasPrefix("Resets "))
    #expect(usage.primary.resetLabel.hasSuffix(" AM") || usage.primary.resetLabel.hasSuffix(" PM"))
    #expect(usage.primary.resetLabel != usage.secondary.resetLabel)
    #expect(UsagePresentation(nil).primary.resetLabel == "Reset time unavailable")
  }

  @Test func bankedResetCountIsAuthoritativeAndDistinctFromMissingData() throws {
    for count: Int64 in [0, 5] {
      let usage = try JSONDecoder().decode(
        CachedUsage.self,
        from: Data(
          """
          {"status":"stale","rateLimits":{"rateLimits":{},
           "rateLimitResetCredits":{"availableCount":\(count),"credits":[{"status":"available"}]}}}
          """.utf8))
      let presentation = UsagePresentation(usage)
      #expect(presentation.bankedResetCount == count)
      #expect(presentation.bankedResetLabel == "\(count) available")
    }
    for credits in ["null", "{}", "{\"availableCount\":-1}"] {
      let usage = try JSONDecoder().decode(
        CachedUsage.self,
        from: Data(
          """
          {"status":"fresh","rateLimits":{"rateLimits":{},"rateLimitResetCredits":\(credits)}}
          """.utf8))
      #expect(UsagePresentation(usage).bankedResetCount == nil)
      #expect(UsagePresentation(usage).bankedResetLabel == "Unavailable")
    }
    #expect(UsagePresentation(nil).bankedResetLabel == "Unavailable")
  }

  @Test func compactResetTimesUseLocalDayAndKeepOtherDatesUnambiguous() throws {
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = try #require(TimeZone(secondsFromGMT: 7 * 3600))
    let formatter = ISO8601DateFormatter()
    let now = try #require(formatter.date(from: "2026-10-20T05:00:00Z"))
    for (timestamp, label) in [
      ("2026-10-20T07:37:00Z", "resets today 2:37 PM"),
      ("2026-10-19T17:00:00Z", "resets today 12:00 AM"),
      ("2026-10-20T05:00:00Z", "resets today 12:00 PM"),
      ("2026-10-25T02:45:00Z", "resets 25 Oct 9:45 AM"),
      ("2026-10-20T18:05:00Z", "resets 21 Oct 1:05 AM"),
      ("2026-10-19T02:45:00Z", "resets 19 Oct 9:45 AM"),
      ("2027-01-01T02:45:00Z", "resets 1 Jan 2027 9:45 AM"),
    ] {
      let date = try #require(formatter.date(from: timestamp))
      let window = try JSONDecoder().decode(
        QuotaWindow.self,
        from: Data("{\"usedPercent\":20,\"resetsAt\":\(Int64(date.timeIntervalSince1970))}".utf8))
      let quota = QuotaPresentation(window: window, fallback: "Primary")
      #expect(quota.compactResetLabel(now: now, calendar: calendar) == label)
    }
    #expect(
      QuotaPresentation(window: nil, fallback: "Primary").compactResetLabel() == "reset unavailable"
    )
  }

  @Test func compactCacheTimesRetainStalePartialAndFailureWarnings() throws {
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = try #require(TimeZone(secondsFromGMT: 7 * 3600))
    let now = try #require(ISO8601DateFormatter().date(from: "2026-10-20T06:00:00Z"))
    let usage = try JSONDecoder().decode(
      CachedUsage.self,
      from: Data(
        #"{"status":"stale","fetchedAt":"2026-10-20T05:38:00Z","partial":["secondary"],"error":"synthetic"}"#
          .utf8))
    #expect(
      UsagePresentation(usage, now: now, calendar: calendar).compactCacheLabel
        == "Stale cache · updated 12:38 PM · Partial · Refresh failed")
    let full = UsagePresentation(usage, now: now, calendar: calendar).cacheLabel
    #expect(full == "Stale cache · 20 Oct 2026 at 12:38 PM · Partial · Refresh failed")
    #expect(UsagePresentation(nil).compactCacheLabel == "Usage unavailable")
  }

}
