using System.Globalization;
using System.Text.Json;

namespace CodexSwitch.Core;

public sealed record UiStatus
{
    public required int SchemaVersion { get; init; }
    public required Account[] Accounts { get; init; }
    public required Observation Observation { get; init; }
    public required CodexProcess[] CodexProcesses { get; init; }

    public static UiStatus Decode(string json)
    {
        try
        {
            using var document = JsonDocument.Parse(json);
            if (document.RootElement.GetProperty("schemaVersion").GetInt32() != 1)
                throw new UiException("This CLI status version is not supported. Use a compatible build.");
            var value = JsonSerializer.Deserialize<UiStatus>(json, JsonOptions)
                ?? throw new JsonException();
            if (value.Accounts is null || value.Observation is null || value.CodexProcesses is null ||
                value.Accounts.Any(a => a is null || string.IsNullOrWhiteSpace(a.Id) ||
                    string.IsNullOrWhiteSpace(a.AccountId) || string.IsNullOrWhiteSpace(a.Alias) || a.Alias.Any(char.IsControl)) ||
                value.Accounts.Select(a => a.Id).Distinct(StringComparer.Ordinal).Count() != value.Accounts.Length ||
                value.CodexProcesses.Any(p => p is null || p.Pid <= 0 || string.IsNullOrWhiteSpace(p.Command)))
                throw new JsonException();
            return value;
        }
        catch (UiException) { throw; }
        catch (Exception e) when (e is JsonException or KeyNotFoundException or InvalidOperationException or FormatException)
        {
            throw new UiException("The CLI returned invalid status. Check the CLI and retry.");
        }
    }

    internal static readonly JsonSerializerOptions JsonOptions = new()
    {
        PropertyNamingPolicy = JsonNamingPolicy.CamelCase,
        AllowDuplicateProperties = false,
        MaxDepth = 64
    };

    public bool CanSwitch => Observation.State switch
    {
        "no_active" or "logged_out" => !Observation.HasLive && !Observation.Managed,
        "in_sync" or "external_login" or "credential_refresh" or "external_login_with_refresh" or "state_drift" =>
            Observation.HasLive && Observation.Managed && Accounts.Any(a =>
                a.Id == Observation.ProfileId && a.AccountId == Observation.AccountId &&
                (a.WorkspaceId is null || a.WorkspaceId == Observation.WorkspaceId)),
        _ => false
    };

    public string? ActiveId => CanSwitch && Observation.HasLive ? Observation.ProfileId : null;

    public string? Notice => Observation.State switch
    {
        "unmanaged" => "The current Codex login is not saved. Run account import-current <alias> with the CLI, then reload.",
        "ambiguous" => "The current login matches multiple saved profiles. Resolve the ambiguous identity with the CLI before switching.",
        "credential_conflict" => "Saved and live credentials conflict. Inspect sync --check with the CLI before switching.",
        "no_active" when CanSwitch => "No account is active. Select a saved account to activate it.",
        "logged_out" when CanSwitch => "Codex is logged out. Select a saved account to activate it.",
        _ when !CanSwitch => "The CLI account state could not be verified. Check account state with the CLI before switching.",
        _ => null
    };
}

public sealed record Account
{
    public required string Id { get; init; }
    public required string Alias { get; init; }
    public required string AccountId { get; init; }
    public string? WorkspaceId { get; init; }
    public CachedUsage? Usage { get; init; }
}

public sealed record Observation
{
    public required string State { get; init; }
    public required bool HasLive { get; init; }
    public required bool Managed { get; init; }
    public string? ProfileId { get; init; }
    public string? AccountId { get; init; }
    public string? WorkspaceId { get; init; }
}

public sealed record CodexProcess
{
    public required int Pid { get; init; }
    public required string Command { get; init; }
}

public sealed record CachedUsage
{
    public string? Status { get; init; }
    public string? FetchedAt { get; init; }
    public string? PlanType { get; init; }
    public RateLimitsEnvelope? RateLimits { get; init; }
    public string[]? Partial { get; init; }
    public string? Error { get; init; }
    public bool IsAvailable => Status is "fresh" or "stale";

    public string CacheLabel(DateTimeOffset now)
    {
        var kind = IsAvailable ? "Cached" : "Usage unavailable";
        if (Partial?.Length > 0) kind += " · partial";
        if (!string.IsNullOrEmpty(Error)) kind += " · last refresh failed";
        if (!DateTimeOffset.TryParse(FetchedAt, CultureInfo.InvariantCulture, DateTimeStyles.RoundtripKind, out var date) ||
            date <= DateTimeOffset.UnixEpoch || date > now)
            return kind + " · update time unavailable";
        var stale = IsAvailable && (Status == "stale" || now - date > TimeSpan.FromMinutes(5));
        return $"{kind}{(stale ? " · stale" : "")} · updated {date.ToLocalTime():MMM d, h:mm tt}";
    }
}

public sealed record RateLimitsEnvelope
{
    public RateLimit? RateLimits { get; init; }
    public Dictionary<string, RateLimit>? RateLimitsByLimitId { get; init; }
    public ResetCredits? RateLimitResetCredits { get; init; }
    public RateLimit? Main => RateLimitsByLimitId?.GetValueOrDefault("codex") ?? RateLimits;
}

public sealed record RateLimit
{
    public QuotaWindow? Primary { get; init; }
    public QuotaWindow? Secondary { get; init; }
}

public sealed record QuotaWindow
{
    public int? UsedPercent { get; init; }
    public int? WindowDurationMins { get; init; }
    public long? ResetsAt { get; init; }
    public int? Remaining => UsedPercent is >= 0 and <= 100 ? 100 - UsedPercent : null;
    public string Title(string fallback) => WindowDurationMins switch
    {
        300 => "5-hour quota",
        10080 => "Weekly quota",
        > 0 => $"{WindowDurationMins}m quota",
        _ => fallback
    };
    public string ResetLabel
    {
        get
        {
            if (ResetsAt is null or <= 0) return "Reset unavailable";
            try { return $"Resets {DateTimeOffset.FromUnixTimeSeconds(ResetsAt.Value).ToLocalTime():MMM d, h:mm tt}"; }
            catch (ArgumentOutOfRangeException) { return "Reset unavailable"; }
        }
    }
}

public sealed record ResetCredits
{
    public long? AvailableCount { get; init; }
}

public static class Selectors
{
    public static bool IsValid(string? alias) => !string.IsNullOrWhiteSpace(alias) &&
        alias.Length <= 64 && alias == alias.Trim() && !alias.StartsWith('-') && !alias.Any(char.IsControl);

    public static bool IsUnambiguous(Account account, UiStatus status) => IsValid(account.Alias) &&
        !status.Accounts.Any(other => other.Id != account.Id &&
            (other.Id == account.Alias || string.Equals(other.Alias, account.Alias, StringComparison.OrdinalIgnoreCase)));
}

public sealed class UiException(string message) : Exception(message) { }
