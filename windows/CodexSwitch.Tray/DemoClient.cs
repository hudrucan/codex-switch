using System;
using System.Linq;
using System.Threading.Tasks;
using CodexSwitch.Core;

namespace CodexSwitch.Tray;

internal sealed class DemoClient(string scenario = "active") : ICliClient
{
    private string active = "demo-work";

    public Task<UiStatus> Status()
    {
        var accounts = new[] { Account("demo-work", "work", 32, 59, "pro"), Account("demo-personal", "personal", 8, 22, "plus") };
        if (scenario == "many") accounts = accounts.Concat(Enumerable.Range(1, 5).Select(i => Account($"extra-{i}", $"account-{i}", 10, 25, "team"))).ToArray();
        return Task.FromResult(new UiStatus
        {
            SchemaVersion = 1,
            Accounts = accounts,
            CodexProcesses = [],
            Observation = scenario == "unmanaged"
                ? new Observation { State = "unmanaged", HasLive = true, Managed = false, AccountId = "unsaved-demo" }
                : new Observation { State = "in_sync", HasLive = true, Managed = true, ProfileId = active, AccountId = active }
        });
    }

    public Task Use(Account account) { active = account.Id; return Task.CompletedTask; }

    public async Task<Account> RefreshUsage(Account account)
    {
        await Task.Delay(150);
        return account with { Usage = account.Usage! with { FetchedAt = DateTimeOffset.UtcNow.ToString("O") } };
    }

    private static Account Account(string id, string alias, int primary, int weekly, string plan) => new()
    {
        Id = id,
        Alias = alias,
        AccountId = id,
        Usage = new CachedUsage
        {
            Status = "fresh",
            FetchedAt = DateTimeOffset.Now.AddMinutes(-2).ToString("O"),
            PlanType = plan,
            RateLimits = new RateLimitsEnvelope
            {
                RateLimits = new RateLimit
                {
                    Primary = new QuotaWindow { UsedPercent = primary, WindowDurationMins = 300, ResetsAt = DateTimeOffset.Now.AddHours(2).ToUnixTimeSeconds() },
                    Secondary = new QuotaWindow { UsedPercent = weekly, WindowDurationMins = 10080, ResetsAt = DateTimeOffset.Now.AddDays(3).ToUnixTimeSeconds() }
                },
                RateLimitResetCredits = new ResetCredits { AvailableCount = id == "demo-work" ? 2 : 0 }
            }
        }
    };
}
