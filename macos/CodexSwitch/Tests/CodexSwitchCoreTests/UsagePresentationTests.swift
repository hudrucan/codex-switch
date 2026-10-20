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
    #expect(usage.primary.accessibilityLabel == "5-hour, 62% remaining, cached")
  }

  @Test func unavailableAndUnknownCacheStatesDoNotDisplayOldQuota() throws {
    for state in ["unavailable", "future_state"] {
      let usage = try JSONDecoder().decode(
        CachedUsage.self,
        from: Data(
          """
          {"status":"\(state)","rateLimits":{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300}}}}
          """.utf8))
      let presentation = UsagePresentation(usage)
      #expect(presentation.primary.remaining == nil)
      #expect(presentation.secondary.remaining == nil)
      #expect(presentation.summary == "Usage unavailable")
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
}
