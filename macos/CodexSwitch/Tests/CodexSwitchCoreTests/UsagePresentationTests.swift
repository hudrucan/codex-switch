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
      #expect(presentation.summary == "Usage unavailable")
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
    #expect(presentation.summary == "Usage unavailable")
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

}
