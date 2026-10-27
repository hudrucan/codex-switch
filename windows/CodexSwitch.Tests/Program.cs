using System.Text.Json;
using System.IO;
using System.Text.Json.Nodes;
using System.Security.AccessControl;
using System.Security.Principal;
using CodexSwitch.Core;

// This executable doubles as a synthetic subprocess. It never runs the real CLI or opens a secret store.
if (args.Length > 0 && args[0] is "ui" or "use" or "account")
{
    switch (Environment.GetEnvironmentVariable("CODEX_SWITCH_TEST_MODE"))
    {
        case "timeout": await Task.Delay(30_000); return 0;
        case "failure": Console.Error.Write("SYNTHETIC_PRIVATE_DIAGNOSTIC"); return 9;
        case "large": Console.Write(new string('x', 4 * 1024 * 1024 + 1)); return 0;
        case "pipes": Console.Error.Write(new string('e', 128 * 1024)); break;
        case "slow-use": if (args[0] == "use") await Task.Delay(300); break;
        case "slow-refresh": if (args[0] == "account") await Task.Delay(300); break;
    }
    if (args[0] == "account")
    {
        if (args.Length != 5 || !args.Take(4).SequenceEqual(["account", "usage", "--json", "--"])) return 9;
        var refreshed = Fixtures.Refreshed(Fixtures.Status().Accounts[1] with { Alias = args[4] });
        var mode = Environment.GetEnvironmentVariable("CODEX_SWITCH_TEST_MODE");
        refreshed = mode switch
        {
            "refresh-profile" => refreshed with { Id = "different-profile" },
            "refresh-alias" => refreshed with { Alias = "different-alias" },
            "refresh-account" => refreshed with { AccountId = "different-account" },
            "refresh-workspace" => refreshed with { WorkspaceId = "different-workspace" },
            "refresh-stale" => refreshed with { Usage = refreshed.Usage! with { Status = "stale" } },
            "refresh-future" => refreshed with { Usage = refreshed.Usage! with { FetchedAt = DateTimeOffset.UtcNow.AddHours(1).ToString("O") } },
            "refresh-error" => refreshed with { Usage = refreshed.Usage! with { Error = "SYNTHETIC_PRIVATE_DIAGNOSTIC" } },
            _ => refreshed
        };
        Console.Write(JsonSerializer.Serialize(refreshed, new JsonSerializerOptions { PropertyNamingPolicy = JsonNamingPolicy.CamelCase }));
        return 0;
    }
    Console.Write(args[0] == "ui" ? Fixtures.Json() :
        JsonSerializer.Serialize(new { changed = true, profileId = "b", alias = args[1] }));
    return 0;
}

var passed = 0;
var failed = 0;
var skipped = 0;
await TestSync("unknown JSON fields remain compatible", () =>
{
    var node = JsonNode.Parse(Fixtures.Json())!;
    node["future"] = new JsonObject { ["nested"] = true };
    node["accounts"]![0]!["email"] = "synthetic@example.invalid";
    Check(UiStatus.Decode(node.ToJsonString()).ActiveId == "a");
});
await TestSync("unsupported schema fails closed", () => ExpectInvalid(Fixtures.Json().Replace("\"schemaVersion\":1", "\"schemaVersion\":2")));
await TestSync("missing required fields do not become safe defaults", () =>
{
    var node = JsonNode.Parse(Fixtures.Json())!;
    node["observation"]!.AsObject().Remove("hasLive");
    ExpectInvalid(node.ToJsonString());
});
await TestSync("duplicate JSON properties are rejected", () => ExpectInvalid(Fixtures.Json().Replace("\"schemaVersion\":1", "\"schemaVersion\":1,\"schemaVersion\":1")));
await TestSync("null and duplicate account arrays are rejected", () =>
{
    var node = JsonNode.Parse(Fixtures.Json())!;
    node["accounts"]!.AsArray().Add(node["accounts"]![0]!.DeepClone());
    ExpectInvalid(node.ToJsonString());
    node["accounts"] = null;
    ExpectInvalid(node.ToJsonString());
});
await TestSync("active identity comes from observation, not recorded active flags", () =>
{
    var status = UiStatus.Decode(Fixtures.Json());
    Check(status.ActiveId == "a");
    Check(!UiStatus.Decode(Fixtures.Json(state: "unmanaged")).CanSwitch);
    Check(!UiStatus.Decode(Fixtures.Json(state: "credential_conflict")).CanSwitch);
    Check(!UiStatus.Decode(Fixtures.Json(state: "future_state")).CanSwitch);
});
await TestSync("unmanaged and conflicting states get distinct actionable notices", () =>
{
    var unmanaged = UiStatus.Decode(Fixtures.Json(state: "unmanaged"));
    Check(!unmanaged.CanSwitch && unmanaged.Notice!.Contains("import-current"));
    var conflict = UiStatus.Decode(Fixtures.Json(state: "credential_conflict"));
    Check(!conflict.CanSwitch && conflict.Notice!.Contains("sync --check"));
    Check(!UiStatus.Decode(Fixtures.Json(state: "ambiguous")).Notice!.Contains("import-current"));
});
await TestSync("account and workspace mismatch block the active marker", () =>
{
    var node = JsonNode.Parse(Fixtures.Json())!;
    node["observation"]!["accountId"] = "another-account";
    Check(UiStatus.Decode(node.ToJsonString()).ActiveId is null);
    node = JsonNode.Parse(Fixtures.Json())!;
    node["accounts"]![0]!["workspaceId"] = "known-workspace";
    Check(!UiStatus.Decode(node.ToJsonString()).CanSwitch);
});
await TestSync("logged out requires no live or managed identity", () =>
{
    var node = JsonNode.Parse(Fixtures.Json(state: "logged_out"))!;
    Check(!UiStatus.Decode(node.ToJsonString()).CanSwitch);
    node["observation"]!["hasLive"] = false;
    node["observation"]!["managed"] = false;
    Check(UiStatus.Decode(node.ToJsonString()).CanSwitch);
});
await TestSync("unsafe aliases stay visible but cannot switch", () =>
{
    var node = JsonNode.Parse(Fixtures.Json())!;
    node["accounts"]![1]!["alias"] = "--allow-running";
    var status = UiStatus.Decode(node.ToJsonString());
    Check(status.Accounts.Length == 2 && !Selectors.IsUnambiguous(status.Accounts[1], status));
    Check(!Selectors.IsValid("trailing ") && !Selectors.IsValid("line\nbreak"));
});
await TestSync("alias and profile ID collisions are blocked", () =>
{
    var node = JsonNode.Parse(Fixtures.Json())!;
    node["accounts"]![1]!["alias"] = "a";
    var status = UiStatus.Decode(node.ToJsonString());
    Check(!Selectors.IsUnambiguous(status.Accounts[1], status));
});
await TestSync("missing quota never displays zero or invents a reset", () =>
{
    Check(new QuotaWindow().Remaining is null);
    Check(new QuotaWindow { UsedPercent = 101 }.Remaining is null);
    Check(new QuotaWindow { UsedPercent = 42 }.Remaining == 58);
    Check(new QuotaWindow { ResetsAt = long.MaxValue }.ResetLabel == "Reset unavailable");
    Check(new ResetCredits().AvailableCount is null);
    Check(new QuotaWindow().Title("Primary quota") == "Primary quota");
    Check(new QuotaWindow { WindowDurationMins = 30 }.Title("Primary quota") == "30m quota");
});
await TestSync("freshness uses timestamps and keeps cache explicit", () =>
{
    var now = DateTimeOffset.Parse("2026-10-27T12:00:00Z");
    Check(new CachedUsage { Status = "fresh", FetchedAt = now.AddHours(-1).ToString("O") }.CacheLabel(now).Contains("stale"));
    Check(new CachedUsage { Status = "fresh", FetchedAt = now.AddHours(1).ToString("O") }.CacheLabel(now).Contains("unavailable"));
    Check(new CachedUsage { Status = "fresh", FetchedAt = now.AddMinutes(-1).ToString("O") }.CacheLabel(now).StartsWith("Cached"));
    var partial = new CachedUsage { Status = "stale", Partial = ["unavailable-method"], Error = "SYNTHETIC_PRIVATE_DIAGNOSTIC" };
    Check(partial.CacheLabel(now).Contains("partial") && !partial.CacheLabel(now).Contains("SYNTHETIC_PRIVATE"));
    Check(!new CachedUsage { Status = "future_status" }.IsAvailable);
});
await Test("opening the UI only reads cached status", async () =>
{
    var fake = new FakeClient();
    await new SwitchController(fake).Reload();
    Check(fake.Trace.SequenceEqual(["status"]));
});
await Test("explicit refresh queries accounts sequentially and re-observes status", async () =>
{
    var fake = new FakeClient { Running = true, AmbiguousState = true };
    var controller = new SwitchController(fake);
    var progress = new List<(string? Alias, int Completed)>();
    controller.RefreshProgressChanged += () => progress.Add((controller.RefreshingAlias, controller.RefreshCompleted));
    await controller.RefreshUsage();
    Check(fake.Trace.SequenceEqual(["status", "refresh:work", "refresh:personal", "status"]));
    Check(controller.RefreshCompleted == 2 && controller.RefreshNotice == "Updated 2 of 2 accounts.");
    Check(progress.Any(p => p.Alias == "work") && progress.Any(p => p.Alias == "personal"));
    Check(progress[^1].Completed == 2 && progress.All(p => p.Completed is >= 0 and <= 2));
    Check(progress.Select(p => p.Completed).SequenceEqual(progress.Select(p => p.Completed).Order()));
    Check(!controller.IsBusy && !controller.IsRefreshing && !controller.Status!.CanSwitch);
    Check(controller.Status!.Accounts.All(a => a.Usage?.Status == "fresh"));
});
await Test("one failed refresh retains cache and does not block the next account", async () =>
{
    var fake = new FakeClient { FailRefreshAlias = "work", InitialCached = true };
    var controller = new SwitchController(fake);
    await controller.RefreshUsage();
    Check(fake.Trace.SequenceEqual(["status", "refresh:work", "refresh:personal", "status"]));
    Check(controller.Status!.Accounts[0].Usage?.Status == "stale" && controller.Status.Accounts[1].Usage?.Status == "fresh");
    Check(controller.RefreshNotice!.Contains("1 of 2") && !controller.Message!.Contains("SYNTHETIC_PRIVATE"));
});
await Test("completed refresh survives a failed final status without enabling switches", async () =>
{
    var controller = new SwitchController(new FakeClient { FailFinalStatus = true });
    await controller.RefreshUsage();
    Check(controller.Status!.Accounts.All(a => a.Usage?.Status == "fresh"));
    Check(!controller.StatusAvailable && !controller.IsBusy && controller.Message is not null);
});
await Test("refresh rejects ambiguous aliases without blocking safe accounts", async () =>
{
    var fake = new FakeClient { CollidingAliases = true };
    var controller = new SwitchController(fake);
    await controller.RefreshUsage();
    Check(fake.Trace.SequenceEqual(["status", "status"]));
    Check(controller.RefreshCompleted == 2 && controller.RefreshNotice == "Updated 0 of 2 accounts.");
});
await Test("refresh cooldown makes no network calls and is checked again after completion", async () =>
{
    var now = DateTimeOffset.UtcNow;
    var fake = new FakeClient();
    var controller = new SwitchController(fake, () => now);
    await controller.RefreshUsage();
    await controller.RefreshUsage();
    Check(fake.Trace.Count == 4 && controller.RefreshNotice!.Contains("60 seconds"));
    now = now.AddSeconds(60);
    await controller.RefreshUsage();
    Check(fake.Trace.Count == 8);
});
await Test("a refresh remains busy across all account calls and final observation", async () =>
{
    var fake = new FakeClient { RefreshGate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously) };
    var controller = new SwitchController(fake);
    var first = controller.RefreshUsage();
    await controller.Reload();
    await controller.Switch(Fixtures.Status().Accounts[1]);
    await controller.RefreshUsage();
    Check(controller.IsBusy && fake.Trace.SequenceEqual(["status", "refresh:work"]));
    fake.RefreshGate.SetResult();
    await first;
    Check(!controller.IsBusy && fake.Trace.SequenceEqual(["status", "refresh:work", "refresh:personal", "status"]));
});
await Test("fresh status precedes use and final observation follows it", async () =>
{
    var fake = new FakeClient();
    var controller = new SwitchController(fake);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    Check(fake.Trace.SequenceEqual(["status", "use:personal", "status"]));
    Check(controller.Status!.ActiveId == "b" && controller.Message!.Contains("switched"));
});
await Test("running Codex blocks use and retry rechecks processes", async () =>
{
    var fake = new FakeClient { Running = true };
    var controller = new SwitchController(fake);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    Check(controller.PendingAccount is not null && !fake.Trace.Any(x => x.StartsWith("use:")));
    fake.Running = false;
    await controller.Switch(controller.PendingAccount!);
    Check(fake.Trace.SequenceEqual(["status", "status", "use:personal", "status"]));
});
await Test("a renamed or reassigned target cannot receive the switch", async () =>
{
    var fake = new FakeClient { Renamed = true };
    await new SwitchController(fake).Switch(Fixtures.Status().Accounts[1]);
    Check(fake.Trace.SequenceEqual(["status"]));
});
await Test("Close & Switch needs confirmation and waits for verified exit", async () =>
{
    var fake = new FakeClient { Running = true };
    var apps = new FakeApplications { OnClose = () => fake.Running = false };
    var controller = new SwitchController(fake, applications: apps, waitForClose: () => Task.CompletedTask);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    Check(controller.CanCloseAndSwitch && apps.Closed.Count == 0 && !fake.Trace.Any(t => t.StartsWith("use:")));
    await controller.CloseAndSwitch();
    Check(apps.Closed.SequenceEqual([900]) && fake.Trace.Count(t => t == "use:personal") == 1);
    Check(controller.Status!.ActiveId == "b" && !controller.IsBusy && controller.PendingAccount is null);
});
await Test("cancel and unknown CLI ownership never request a close", async () =>
{
    var fake = new FakeClient { Running = true };
    var apps = new FakeApplications();
    var controller = new SwitchController(fake, applications: apps);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    controller.CancelSwitch();
    await controller.CloseAndSwitch();
    Check(apps.Closed.Count == 0 && controller.PendingAccount is null);
    apps.Targets.Clear();
    await controller.Switch(Fixtures.Status().Accounts[1]);
    await controller.CloseAndSwitch();
    Check(!controller.CanCloseAndSwitch && apps.Closed.Count == 0 && controller.PendingAccount is not null);
});
await Test("close confirmation rejects reused process generations and changed accounts", async () =>
{
    foreach (var change in new[] { "generation", "account", "unknown", "status" })
    {
        var fake = new FakeClient { Running = true };
        var apps = new FakeApplications();
        var controller = new SwitchController(fake, applications: apps);
        await controller.Switch(Fixtures.Status().Accounts[1]);
        if (change == "generation") apps.Targets[1234] = apps.Targets[1234] with { StartedAt = 2 };
        if (change == "account") fake.Renamed = true;
        if (change == "unknown") apps.Targets.Clear();
        if (change == "status") fake.FailStatus = true;
        await controller.CloseAndSwitch();
        Check(apps.Closed.Count == 0 && !fake.Trace.Any(t => t.StartsWith("use:")));
    }
});
await Test("declined or slow closes retain manual retry without force switching", async () =>
{
    foreach (var mode in new[] { "decline", "slow", "failure" })
    {
        var fake = new FakeClient { Running = true };
        var apps = new FakeApplications { AcceptClose = mode != "decline" };
        if (mode == "failure") apps.OnClose = () => throw new Exception("SYNTHETIC_PRIVATE_DIAGNOSTIC");
        var controller = new SwitchController(fake, applications: apps, waitForClose: () => Task.CompletedTask);
        await controller.Switch(Fixtures.Status().Accounts[1]);
        await controller.CloseAndSwitch();
        Check(apps.Closed.Count == 1 && controller.PendingAccount is not null && !controller.CanCloseAndSwitch);
        Check(!fake.Trace.Any(t => t.StartsWith("use:")) && !controller.IsBusy);
        Check(!controller.Message!.Contains("SYNTHETIC_PRIVATE"));
    }
});
await Test("failed observation or changed account after closing prevents switching", async () =>
{
    foreach (var mode in new[] { "account", "status" })
    {
        var fake = new FakeClient { Running = true };
        var apps = new FakeApplications { OnClose = () => { fake.Running = false; fake.Renamed = mode == "account"; fake.FailStatus = mode == "status"; } };
        var controller = new SwitchController(fake, applications: apps);
        await controller.Switch(Fixtures.Status().Accounts[1]);
        await controller.CloseAndSwitch();
        Check(apps.Closed.Count == 1 && !fake.Trace.Any(t => t.StartsWith("use:")));
    }
});
await Test("new processes while waiting require fresh confirmation", async () =>
{
    var fake = new FakeClient { Running = true };
    var apps = new FakeApplications();
    apps.OnClose = () => apps.Targets[1234] = apps.Targets[1234] with { StartedAt = 2 };
    var controller = new SwitchController(fake, applications: apps, waitForClose: () => Task.CompletedTask);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    await controller.CloseAndSwitch();
    Check(apps.Closed.Count == 1 && controller.CanCloseAndSwitch && !fake.Trace.Any(t => t.StartsWith("use:")));
});
await Test("each GUI close revalidates the remaining consented applications", async () =>
{
    var fake = new FakeClient { Processes = [new() { Pid = 1234, Command = "Codex.exe" }, new() { Pid = 2345, Command = "Codex.exe" }] };
    var apps = new FakeApplications();
    apps.Targets[2345] = new(901, 1, @"C:\Synthetic\Codex.exe", (nint)11);
    apps.OnClose = () =>
    {
        fake.Processes = [new() { Pid = 2345, Command = "Codex.exe" }];
        apps.Targets[2345] = apps.Targets[2345] with { StartedAt = 2 };
    };
    var controller = new SwitchController(fake, applications: apps);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    await controller.CloseAndSwitch();
    Check(apps.Closed.SequenceEqual([900]) && !fake.Trace.Any(t => t.StartsWith("use:")));
});
await Test("Close & Switch stays busy and retains the final CLI process guard", async () =>
{
    var fake = new FakeClient { Running = true };
    var gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
    var apps = new FakeApplications();
    var controller = new SwitchController(fake, applications: apps, waitForClose: () => gate.Task);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    var close = controller.CloseAndSwitch();
    Check(controller.IsBusy && controller.IsClosing);
    await controller.RefreshUsage();
    controller.CancelSwitch();
    await controller.CloseAndSwitch();
    Check(apps.Closed.Count == 1);
    fake.Running = false;
    fake.Race = true;
    gate.SetResult();
    await close;
    Check(controller.PendingAccount is not null && fake.Trace.Count(t => t.StartsWith("use:")) == 1);
    Check(!controller.IsBusy && !controller.Message!.Contains("SYNTHETIC_PRIVATE"));
});
await TestSync("Windows app ownership resolves only stable same-user Store ancestry", () =>
{
    var root = new DesktopProcess(900, 1, 100, @"C:\Apps\Codex\Codex.exe", ApplicationOwnership.SupportedPackage, (nint)10, true);
    var child = new DesktopProcess(1234, 900, 101, @"C:\Apps\Codex\resources\codex.exe", "", 0, true);
    var records = new Dictionary<int, DesktopProcess> { [900] = root, [1234] = child };
    var reported = new CodexProcess { Pid = 1234, Command = "codex.exe" };
    ApplicationTarget? Identify() => ApplicationOwnership.Identify(reported, pid => records.GetValueOrDefault(pid), @"C:\User\OpenAI\Codex\bin");
    Check(Identify()?.Pid == 900);
    records[1234] = child with { Executable = @"C:\User\OpenAI\Codex\bin\hash\codex.exe" };
    Check(Identify()?.Pid == 900);
    foreach (var changed in new[] { child with { IsCurrentUserSession = false }, child with { ParentPid = 1234 },
        child with { StartedAt = 99 }, child with { Executable = @"C:\Apps\CodexOther\codex.exe" },
        child with { Executable = @"C:\Standalone\codex.exe" }, child with { PackageFamily = "unknown" } })
    {
        records[1234] = changed;
        Check(Identify() is null);
    }
    records[1234] = child;
    records[900] = root with { PackageFamily = "unknown" };
    Check(Identify() is null);
    records[900] = root with { Window = 0 };
    Check(Identify() is null);
    records[900] = root with { IsCurrentUserSession = false };
    Check(Identify() is null);
    records[900] = root;
    var reads = 0;
    Check(ApplicationOwnership.Identify(reported, pid => ++reads > 2 ? records[pid] with { StartedAt = 999 } : records[pid], @"C:\User\OpenAI\Codex\bin") is null);
});
await Test("process-start race returns to manual retry without bypass", async () =>
{
    var fake = new FakeClient { Race = true };
    var controller = new SwitchController(fake);
    await controller.Switch(Fixtures.Status().Accounts[1]);
    Check(controller.PendingAccount is not null && fake.Trace.Count(x => x.StartsWith("use:")) == 1);
});
await Test("failed cached reads preserve data but disable stale status", async () =>
{
    var fake = new FakeClient();
    var controller = new SwitchController(fake);
    await controller.Reload();
    fake.FailStatus = true;
    await controller.Reload();
    Check(controller.Status is not null && !controller.StatusAvailable);
    Check(!controller.Message!.Contains("SYNTHETIC_PRIVATE"));
});
await Test("overlapping UI operations do not issue another command", async () =>
{
    var fake = new FakeClient { Gate = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously) };
    var controller = new SwitchController(fake);
    var first = controller.Reload();
    await controller.Switch(Fixtures.Status().Accounts[1]);
    await controller.Reload();
    Check(controller.IsBusy && fake.Trace.SequenceEqual(["status"]));
    fake.Gate.SetResult();
    await first;
    Check(!controller.IsBusy);
});
await TestSync("missing executables and directories fail without PATH fallback", () =>
{
    ExpectUiError(() => CliResolver.Resolve(AppContext.BaseDirectory, "relative.exe"));
    ExpectUiError(() => CliResolver.Resolve(AppContext.BaseDirectory, @"\\server\share\codex-switch.exe"));
    var directory = Path.Combine(Path.GetTempPath(), "codex-switch-ui-test-" + Guid.NewGuid().ToString("N"));
    Directory.CreateDirectory(directory);
    try
    {
        ExpectUiError(() => CliResolver.Resolve(directory, null));
        Directory.CreateDirectory(Path.Combine(directory, "directory.exe"));
        ExpectUiError(() => CliResolver.Resolve(directory, Path.Combine(directory, "directory.exe")));
    }
    finally { Directory.Delete(directory, recursive: true); }
});
await TestSync("ACLs reject another user's write or replacement rights", () =>
{
    var system = new SecurityIdentifier("S-1-5-18");
    var users = new SecurityIdentifier("S-1-5-32-545");
    var trusted = new HashSet<string> { system.Value };
    var file = new FileSecurity();
    file.SetOwner(system);
    file.AddAccessRule(new FileSystemAccessRule(system, FileSystemRights.FullControl, AccessControlType.Allow));
    CliResolver.ValidateAcl(file, trusted, isDirectory: false);
    file.AddAccessRule(new FileSystemAccessRule(users, FileSystemRights.WriteData, AccessControlType.Allow));
    ExpectUiError(() => CliResolver.ValidateAcl(file, trusted, isDirectory: false));
    var directory = new DirectorySecurity();
    directory.SetOwner(system);
    directory.AddAccessRule(new FileSystemAccessRule(users, FileSystemRights.CreateDirectories, AccessControlType.Allow));
    CliResolver.ValidateAcl(directory, trusted, isDirectory: true);
    directory.AddAccessRule(new FileSystemAccessRule(users, FileSystemRights.DeleteSubdirectoriesAndFiles, AccessControlType.Allow));
    ExpectUiError(() => CliResolver.ValidateAcl(directory, trusted, isDirectory: true));
});
await TestSync("reparse-point executable overrides are rejected", () =>
{
    var directory = Path.Combine(Path.GetTempPath(), "codex-switch-ui-test-" + Guid.NewGuid().ToString("N"));
    Directory.CreateDirectory(directory);
    var target = Path.Combine(directory, "target.exe");
    var link = Path.Combine(directory, "linked.exe");
    try
    {
        File.WriteAllText(target, "synthetic executable fixture; never executed");
        try { File.CreateSymbolicLink(link, target); }
        catch (Exception error) when (error is UnauthorizedAccessException or IOException)
        {
            throw new SkippedTest("Windows symbolic-link creation is unavailable to this test user.");
        }
        ExpectUiError(() => CliResolver.Resolve(directory, link));
    }
    finally
    {
        if (File.Exists(link)) File.Delete(link);
        File.Delete(target);
        Directory.Delete(directory);
    }
});
await Test("real subprocess drains stderr and preserves argument boundaries", async () =>
{
    using var homes = new IsolatedEnvironment("pipes");
    var client = SyntheticClient();
    Check((await client.Status()).Accounts.Length == 2);
    await client.Use(Fixtures.Status().Accounts[1] with { Alias = "personal with spaces" });
});
await Test("subprocess errors never expose raw stderr", async () =>
{
    using var homes = new IsolatedEnvironment("failure");
    try { await SyntheticClient().Status(); throw new Exception("Expected failure"); }
    catch (UiException e) { Check(!e.ToString().Contains("SYNTHETIC_PRIVATE")); }
});
await Test("oversized output is rejected after safe drain", async () =>
{
    using var homes = new IsolatedEnvironment("large");
    try { await SyntheticClient().Status(); throw new Exception("Expected failure"); }
    catch (UiException e) { Check(e.Message.Contains("size")); }
});
await Test("read-only timeout terminates the owned synthetic child", async () =>
{
    using var homes = new IsolatedEnvironment("timeout");
    var client = new CliClient(new CliProcess(() => Environment.ProcessPath!, TimeSpan.FromMilliseconds(150)));
    try { await client.Status(); throw new Exception("Expected timeout"); }
    catch (UiException e) { Check(e.Message.Contains("timed out")); }
});

await Test("a switch is not canceled by the status timeout", async () =>
{
    using var homes = new IsolatedEnvironment("slow-use");
    var client = new CliClient(new CliProcess(() => Environment.ProcessPath!, TimeSpan.FromMilliseconds(50)));
    await client.Use(Fixtures.Status().Accounts[1]);
});
await Test("refresh preserves argument boundaries and outlives the status timeout", async () =>
{
    using var homes = new IsolatedEnvironment("slow-refresh");
    var client = new CliClient(new CliProcess(() => Environment.ProcessPath!, TimeSpan.FromMilliseconds(50)));
    var selected = Fixtures.Status().Accounts[1] with { Alias = "personal with spaces" };
    Check((await client.RefreshUsage(selected)).Alias == selected.Alias);
});
await Test("refresh rejects another identity, stale, future, and failed responses", async () =>
{
    foreach (var mode in new[] { "refresh-profile", "refresh-alias", "refresh-account", "refresh-workspace", "refresh-stale", "refresh-future", "refresh-error", "failure" })
    {
        using var homes = new IsolatedEnvironment(mode);
        try { await SyntheticClient().RefreshUsage(Fixtures.Status().Accounts[1]); throw new Exception("Expected refresh failure"); }
        catch (UiException e) { Check(!e.ToString().Contains("SYNTHETIC_PRIVATE")); }
    }
});

await Test("native WPF menu keeps short lists unscrolled and long lists accessible", () =>
{
    var completion = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
    var thread = new Thread(() =>
    {
        var app = new CodexSwitch.Tray.App();
        try
        {
            app.InitializeComponent();
            using var trayIcon = CodexSwitch.Tray.TrayIcon.Create();
            Check(trayIcon.Width is >= 16 and <= 64 && trayIcon.Width == trayIcon.Height);
            var trayPixels = new byte[16 * 16 * 4];
            CodexSwitch.Tray.TrayIcon.Render(16).CopyPixels(trayPixels, 16 * 4, 0);
            var columns = new HashSet<int>();
            var solidPixels = 0;
            for (var pixel = 0; pixel < 16 * 16; pixel++)
            {
                if (trayPixels[pixel * 4 + 3] >= 64) columns.Add(pixel % 16);
                if (trayPixels[pixel * 4 + 3] >= 224) solidPixels++;
            }
            Check(columns.Count >= 14 && solidPixels >= 35);
            foreach (var scenario in new[] { "active", "unmanaged", "many", "closing" })
                foreach (var dark in new[] { false, true })
                {
                    CodexSwitch.Tray.Palette.Apply(dark);
                    var demo = new CodexSwitch.Tray.DemoClient(scenario);
                    var controller = new SwitchController(demo, applications: demo);
                    var window = new CodexSwitch.Tray.MainWindow(controller, isDemo: true);
                    window.LoadStatus().GetAwaiter().GetResult();
                    if (scenario == "closing") window.SelectAccount(demo.Status().Result.Accounts[1]).GetAwaiter().GetResult();
                    Check(window.Icon is System.Windows.Media.Imaging.BitmapFrame { PixelWidth: 256 });
                    var content = (System.Windows.FrameworkElement)window.Content;
                    content.Measure(new System.Windows.Size(400, double.PositiveInfinity));
                    content.Arrange(new System.Windows.Rect(0, 0, 400, content.DesiredSize.Height));
                    content.UpdateLayout();
                    var labels = Descendants(content).OfType<System.Windows.Controls.TextBlock>().ToArray();
                    foreach (var pair in new[] { ("work", "pro"), ("personal", "plus") })
                    {
                        var alias = labels.Single(l => l.Text == pair.Item1);
                        var plan = labels.Single(l => l.Text == pair.Item2);
                        var title = (System.Windows.FrameworkElement)System.Windows.Media.VisualTreeHelper.GetParent(alias);
                        var bitmap = new System.Windows.Media.Imaging.RenderTargetBitmap((int)Math.Ceiling(title.ActualWidth), (int)Math.Ceiling(title.ActualHeight), 96, 96, System.Windows.Media.PixelFormats.Pbgra32);
                        var visual = new System.Windows.Media.DrawingVisual();
                        using (var drawing = visual.RenderOpen())
                            drawing.DrawRectangle(new System.Windows.Media.VisualBrush(title), null, new System.Windows.Rect(0, 0, title.ActualWidth, title.ActualHeight));
                        bitmap.Render(visual);
                        var delta = Math.Abs(InkCenter(bitmap, ((System.Windows.Media.SolidColorBrush)alias.Foreground).Color) - InkCenter(bitmap, ((System.Windows.Media.SolidColorBrush)plan.Foreground).Color));
                        Console.WriteLine($"INK CENTER {scenario} dark={dark} alias={pair.Item1} delta={delta:F1}px");
                        Check(delta <= 1);
                    }
                    var scroll = (System.Windows.Controls.ScrollViewer)window.FindName("SavedAccountsScroll");
                    Console.WriteLine($"LAYOUT {scenario} dark={dark} height={content.ActualHeight:F0} scrollbar={scroll.ComputedVerticalScrollBarVisibility} viewport={scroll.ViewportHeight:F0} extent={scroll.ExtentHeight:F0}");
                    if ((scroll.ComputedVerticalScrollBarVisibility == System.Windows.Visibility.Visible) != (scenario == "many"))
                        throw new Exception($"Unexpected scrollbar for {scenario}, dark={dark}.");
                    if (content.ActualHeight >= 760) throw new Exception($"Menu exceeds height limit for {scenario}.");
                    if (scenario == "many")
                    {
                        var bar = (System.Windows.Controls.Primitives.ScrollBar)scroll.Template.FindName("PART_VerticalScrollBar", scroll);
                        var track = (System.Windows.Controls.Primitives.Track)bar.Template.FindName("PART_Track", bar);
                        Console.WriteLine($"SCROLLBAR bar={bar.ActualWidth:F0} track={track.ActualWidth:F0} thumb={track.Thumb.ActualWidth:F0} orientation={track.Orientation}");
                        Check(bar.ActualWidth <= 6 && track.Orientation == System.Windows.Controls.Orientation.Vertical && track.Thumb.ActualWidth <= 6);
                    }
                    var notice = (System.Windows.Controls.TextBlock)window.FindName("Notice");
                    if (scenario == "unmanaged") Check(notice.Text.Contains("import-current"));
                    if (scenario == "closing")
                    {
                        var close = (System.Windows.Controls.Button)window.FindName("CloseSwitch");
                        CheckConfirmationLayout(window, "CloseSwitch", "RetrySwitch");
                        Check(close.Visibility == System.Windows.Visibility.Visible && controller.Status!.CodexProcesses.Length == 1);
                        ((System.Windows.Controls.Button)window.FindName("Cancel")).RaiseEvent(new System.Windows.RoutedEventArgs(System.Windows.Controls.Button.ClickEvent));
                        Check(controller.PendingAccount is null && controller.Status!.CodexProcesses.Length == 1);
                        window.SelectAccount(demo.Status().Result.Accounts[1]).GetAwaiter().GetResult();
                        close.RaiseEvent(new System.Windows.RoutedEventArgs(System.Windows.Controls.Button.ClickEvent));
                        Check(controller.Status!.ActiveId == "demo-personal" && controller.Status.CodexProcesses.Length == 0);
                    }
                    window.CloseForExit();
                }
            var manualController = new SwitchController(new FakeClient { Running = true });
            var manualWindow = new CodexSwitch.Tray.MainWindow(manualController, isDemo: true);
            manualWindow.LoadStatus().GetAwaiter().GetResult();
            manualWindow.SelectAccount(Fixtures.Status().Accounts[1]).GetAwaiter().GetResult();
            CheckConfirmationLayout(manualWindow, "RetrySwitch", "CloseSwitch");
            manualWindow.CloseForExit();
            var refreshFake = new FakeClient();
            var refreshController = new SwitchController(refreshFake);
            var refreshWindow = new CodexSwitch.Tray.MainWindow(refreshController, isDemo: true);
            refreshWindow.LoadStatus().GetAwaiter().GetResult();
            Check(refreshFake.Trace.SequenceEqual(["status"]));
            var refreshButton = (System.Windows.Controls.Button)refreshWindow.FindName("Refresh");
            refreshButton.RaiseEvent(new System.Windows.RoutedEventArgs(System.Windows.Controls.Button.ClickEvent));
            Check(refreshFake.Trace.SequenceEqual(["status", "status", "refresh:work", "refresh:personal", "status"]));
            var feedback = (System.Windows.Controls.TextBlock)refreshWindow.FindName("RefreshFeedback");
            Check(feedback.Text == "Updated 2 of 2 accounts.");
            refreshWindow.CloseForExit();
            completion.SetResult();
        }
        catch (Exception error) { completion.SetException(error); }
        finally { app.Shutdown(); }
    })
    { IsBackground = true };
    thread.SetApartmentState(ApartmentState.STA);
    thread.Start();
    return completion.Task.WaitAsync(TimeSpan.FromSeconds(15));
});

Console.WriteLine($"RESULT: {passed} passed, {failed} failed, {skipped} skipped");
return failed == 0 ? 0 : 1;

async Task Test(string name, Func<Task> test)
{
    try { await test(); Console.WriteLine("PASS " + name); passed++; }
    catch (SkippedTest reason) { Console.WriteLine($"SKIP {name}: {reason.Message}"); skipped++; }
    catch (Exception error) { Console.WriteLine($"FAIL {name}: {error.Message}"); failed++; }
}

// Synchronous assertions share the same tiny runner without an external test dependency.
Task TestSync(string name, Action action) => Test(name, () => { action(); return Task.CompletedTask; });
void Check(bool condition) { if (!condition) throw new Exception("Assertion failed"); }
void ExpectInvalid(string json) => ExpectUiError(() => UiStatus.Decode(json));
void ExpectUiError(Action action)
{
    try { action(); }
    catch (UiException) { return; }
    throw new Exception("Expected a safe UI error");
}
CliClient SyntheticClient() => new(new CliProcess(() => Environment.ProcessPath!));

void CheckConfirmationLayout(CodexSwitch.Tray.MainWindow window, string primaryName, string hiddenName)
{
    var cancel = (System.Windows.Controls.Button)window.FindName("Cancel");
    var primary = (System.Windows.Controls.Button)window.FindName(primaryName);
    var hidden = (System.Windows.Controls.Button)window.FindName(hiddenName);
    Check(primary.Visibility == System.Windows.Visibility.Visible && hidden.Visibility == System.Windows.Visibility.Collapsed);
    Check(!((System.Windows.Controls.StackPanel)window.FindName("Actions")).IsEnabled);
    Check(!((System.Windows.Controls.Button)window.FindName("Quit")).IsEnabled);
    var accounts = (System.Windows.Controls.StackPanel)window.FindName("SavedPanel");
    Check(accounts.Children.OfType<System.Windows.Controls.Button>().All(b => !b.IsEnabled));
    var content = (System.Windows.FrameworkElement)window.Content;
    foreach (var width in new[] { 400, 280 })
    {
        content.Measure(new System.Windows.Size(width, double.PositiveInfinity));
        content.Arrange(new System.Windows.Rect(0, 0, width, content.DesiredSize.Height));
        content.UpdateLayout();
        var left = cancel.TranslatePoint(new System.Windows.Point(), content);
        var right = primary.TranslatePoint(new System.Windows.Point(), content);
        Check(cancel.IsEnabled && primary.IsEnabled && cancel.ActualWidth > 0 && primary.ActualWidth > 0);
        Check(Math.Abs(left.Y - right.Y) < 1 && right.X >= left.X + cancel.ActualWidth + 7);
        Check(right.X + primary.ActualWidth <= width - 12);
        Console.WriteLine($"CONFIRMATION {primaryName} width={width}: Cancel and one primary action share a row");
    }
}

IEnumerable<System.Windows.DependencyObject> Descendants(System.Windows.DependencyObject root)
{
    for (var i = 0; i < System.Windows.Media.VisualTreeHelper.GetChildrenCount(root); i++)
    {
        var child = System.Windows.Media.VisualTreeHelper.GetChild(root, i);
        yield return child;
        foreach (var descendant in Descendants(child)) yield return descendant;
    }
}

double InkCenter(System.Windows.Media.Imaging.BitmapSource bitmap, System.Windows.Media.Color color)
{
    var pixels = new byte[bitmap.PixelWidth * bitmap.PixelHeight * 4];
    bitmap.CopyPixels(pixels, bitmap.PixelWidth * 4, 0);
    var rows = new List<int>();
    for (var i = 0; i < bitmap.PixelWidth * bitmap.PixelHeight; i++)
    {
        var alpha = pixels[i * 4 + 3];
        if (alpha < 64) continue;
        if (Math.Abs(pixels[i * 4] * 255d / alpha - color.B) < 4 &&
            Math.Abs(pixels[i * 4 + 1] * 255d / alpha - color.G) < 4 &&
            Math.Abs(pixels[i * 4 + 2] * 255d / alpha - color.R) < 4) rows.Add(i / bitmap.PixelWidth);
    }
    if (rows.Count == 0) throw new Exception("No rendered text pixels matched the expected foreground.");
    return (rows.Min() + rows.Max()) / 2d;
}

internal static class Fixtures
{
    public static string Json(string state = "in_sync", string active = "a", bool running = false) => $$"""
        {"schemaVersion":1,"accounts":[
          {"id":"a","alias":"work","accountId":"account-a","active":false},
          {"id":"b","alias":"personal","accountId":"account-b","active":true}],
         "observation":{"state":"{{state}}","hasLive":true,"managed":true,"profileId":"{{active}}","accountId":"account-{{active}}"},
         "codexProcesses":{{(running ? "[{\"pid\":1234,\"command\":\"codex.exe\"}]" : "[]")}}}
        """;
    public static UiStatus Status() => UiStatus.Decode(Json());
    public static Account Refreshed(Account account) => account with
    {
        Usage = new CachedUsage { Status = "fresh", FetchedAt = DateTimeOffset.UtcNow.ToString("O"), PlanType = "team" }
    };
}

internal sealed class SkippedTest(string reason) : Exception(reason) { }

internal sealed class FakeApplications : IApplicationControl
{
    public Dictionary<int, ApplicationTarget> Targets { get; } = new() { [1234] = new(900, 1, @"C:\Synthetic\Codex.exe", (nint)10) };
    public List<int> Closed { get; } = [];
    public bool AcceptClose { get; init; } = true;
    public Action? OnClose { get; set; }
    public ApplicationTarget? Identify(CodexProcess process) => Targets.GetValueOrDefault(process.Pid);
    public bool RequestClose(ApplicationTarget target) { Closed.Add(target.Pid); OnClose?.Invoke(); return AcceptClose; }
}

internal sealed class FakeClient : ICliClient
{
    public List<string> Trace { get; } = [];
    public bool Running { get; set; }
    public CodexProcess[]? Processes { get; set; }
    public bool Race { get; set; }
    public bool FailStatus { get; set; }
    public bool Renamed { get; set; }
    public bool AmbiguousState { get; init; }
    public bool CollidingAliases { get; init; }
    public bool InitialCached { get; init; }
    public bool FailFinalStatus { get; init; }
    public string? FailRefreshAlias { get; init; }
    public TaskCompletionSource? Gate { get; init; }
    public TaskCompletionSource? RefreshGate { get; init; }
    private readonly Dictionary<string, CachedUsage?> usage = [];
    private int statusCalls;
    private string active = "a";
    public async Task<UiStatus> Status()
    {
        Trace.Add("status");
        statusCalls++;
        if (Gate is not null) await Gate.Task;
        if (FailStatus || FailFinalStatus && statusCalls > 1) throw new Exception("SYNTHETIC_PRIVATE_DIAGNOSTIC");
        var status = UiStatus.Decode(Fixtures.Json(state: AmbiguousState ? "ambiguous" : "in_sync", active: active, running: Running));
        if (Processes is not null) status = status with { CodexProcesses = Processes };
        status = status with
        {
            Accounts = status.Accounts.Select(a => a with
            {
                Usage = usage.GetValueOrDefault(a.Id) ?? (InitialCached ? new CachedUsage { Status = "stale", FetchedAt = DateTimeOffset.UtcNow.AddDays(-1).ToString("O") } : null),
                Alias = CollidingAliases ? "same" : a.Alias
            }).ToArray()
        };
        return Renamed ? status with { Accounts = status.Accounts.Select(a => a.Id == "b" ? a with { Alias = "renamed" } : a).ToArray() } : status;
    }
    public Task Use(Account account)
    {
        Trace.Add("use:" + account.Alias);
        if (Race) { Running = true; throw new Exception("SYNTHETIC_PRIVATE_DIAGNOSTIC"); }
        active = account.Id;
        return Task.CompletedTask;
    }
    public async Task<Account> RefreshUsage(Account account)
    {
        Trace.Add("refresh:" + account.Alias);
        if (RefreshGate is not null) await RefreshGate.Task;
        if (account.Alias == FailRefreshAlias) throw new Exception("SYNTHETIC_PRIVATE_DIAGNOSTIC");
        var updated = Fixtures.Refreshed(account);
        usage[account.Id] = updated.Usage;
        return updated;
    }
}

internal sealed class IsolatedEnvironment : IDisposable
{
    private readonly Dictionary<string, string?> previous = [];
    private readonly string root = Path.Combine(Path.GetTempPath(), "codex-switch-ui-test-" + Guid.NewGuid().ToString("N"));
    public IsolatedEnvironment(string mode)
    {
        Directory.CreateDirectory(root);
        Set("CODEX_HOME", Path.Combine(root, "codex"));
        Set("CODEX_SWITCH_HOME", Path.Combine(root, "switch"));
        Set("CODEX_SWITCH_TEST_MODE", mode);
        Directory.CreateDirectory(Path.Combine(root, "codex"));
        Directory.CreateDirectory(Path.Combine(root, "switch"));
    }
    private void Set(string key, string value) { previous[key] = Environment.GetEnvironmentVariable(key); Environment.SetEnvironmentVariable(key, value); }
    public void Dispose()
    {
        foreach (var (key, value) in previous) Environment.SetEnvironmentVariable(key, value);
        Directory.Delete(root, recursive: true);
    }
}
