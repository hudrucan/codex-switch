import Foundation

public struct UIStatus: Decodable, Equatable, Sendable {
  public let schemaVersion: Int
  public let accounts: [Account]
  public let observation: Observation
  public let codexProcesses: [CodexProcess]

  public static func decode(_ data: Data) throws -> UIStatus {
    do {
      // Check the version before decoding version-specific fields.
      struct Version: Decodable { let schemaVersion: Int }
      let decoder = JSONDecoder()
      let version = try decoder.decode(Version.self, from: data).schemaVersion
      guard version == 1 else { throw UIError.unsupportedSchema }
      return try decoder.decode(UIStatus.self, from: data)
    } catch let error as UIError {
      throw error
    } catch {
      throw UIError.invalidResponse
    }
  }

  public var canSwitch: Bool {
    switch observation.state {
    case "no_active", "logged_out":
      return !observation.hasLive && !observation.managed
    case "in_sync", "external_login", "credential_refresh", "external_login_with_refresh",
      "state_drift":
      return observation.hasLive && observation.managed
        && accounts.contains { $0.id == observation.profileId }
    default:
      return false
    }
  }

  public var activeAccountID: String? {
    canSwitch && observation.hasLive ? observation.profileId : nil
  }

  public var observationNotice: String? {
    switch observation.state {
    case "unmanaged":
      return "The live Codex login is unmanaged. Resolve it with the CLI before switching."
    case "ambiguous":
      return "The live account is ambiguous. Resolve it with the CLI before switching."
    case "credential_conflict":
      return "Credentials conflict. Review them with the CLI before switching."
    case "no_active": return "No account is active."
    case "logged_out": return "Codex is logged out. Recorded account state is stale."
    default:
      if !canSwitch {
        return "Account state is not supported for switching. Review it with the CLI."
      }
      return observation.needsSync
        ? "Live account observed; recorded state needs synchronization." : nil
    }
  }
}

public struct Account: Decodable, Equatable, Identifiable, Sendable {
  public let id: String
  public let alias: String
  public let accountId: String
  public let workspaceId: String?
  public let email: String?
  public let source: String
  public let active: Bool
  public let usage: CachedUsage?
}

public struct Observation: Decodable, Equatable, Sendable {
  public let state: String
  public let credentialState: String?
  public let hasLive: Bool
  public let managed: Bool
  public let needsSync: Bool
  public let profileId: String?
  public let alias: String?
  public let accountId: String?
  public let workspaceId: String?
  public let email: String?
  public let recordedProfileId: String?
  public let recordedAlias: String?
}

public struct CodexProcess: Decodable, Equatable, Hashable, Sendable {
  public let pid: Int32
  public let command: String
}

public struct CachedUsage: Decodable, Equatable, Sendable {
  public let status: String
  public let fetchedAt: String?
  public let planType: String?
  public let rateLimits: RateLimits?
  public let partial: [String]?
  public let error: String?

  public var isStaleOrUnavailable: Bool { status != "fresh" }

  public var summary: String {
    guard status == "fresh" || status == "stale" else { return "Usage unavailable" }
    var parts = [planType?.capitalized ?? "Plan unknown"]
    if let limit = rateLimits?.main {
      if let primary = limit.primary { parts.append(primary.summary(fallback: "Primary")) }
      if let secondary = limit.secondary { parts.append(secondary.summary(fallback: "Secondary")) }
    }
    parts.append(status == "stale" ? "Stale cache" : "Cached")
    if partial?.isEmpty == false { parts.append("Partial") }
    if error?.isEmpty == false { parts.append("Refresh failed") }
    return parts.joined(separator: " · ")
  }

  public var updatedLabel: String {
    guard let fetchedAt else { return "No cached update time" }
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let fractional = formatter.date(from: fetchedAt)
    formatter.formatOptions = [.withInternetDateTime]
    guard let date = fractional ?? formatter.date(from: fetchedAt), date.timeIntervalSince1970 > 0
    else {
      return "No cached update time"
    }
    return "Cached " + date.formatted(date: .abbreviated, time: .shortened)
  }
}

public struct RateLimits: Decodable, Equatable, Sendable {
  public let rateLimits: RateLimit
  public let rateLimitsByLimitId: [String: RateLimit]?
  public var main: RateLimit { rateLimitsByLimitId?["codex"] ?? rateLimits }
}

public struct RateLimit: Decodable, Equatable, Sendable {
  public let primary: QuotaWindow?
  public let secondary: QuotaWindow?
}

public struct QuotaWindow: Decodable, Equatable, Sendable {
  public let usedPercent: Int
  public let windowDurationMins: Int?
  public let resetsAt: Int64?

  func summary(fallback: String) -> String {
    let label: String
    switch windowDurationMins {
    case 300: label = "5h"
    case 10080: label = "Week"
    case let minutes? where minutes > 0: label = "\(minutes)m"
    default: label = fallback
    }
    // Clamp without arithmetic on an untrusted, potentially extreme integer.
    return "\(label) \(100 - min(100, max(0, usedPercent)))% left"
  }
}
