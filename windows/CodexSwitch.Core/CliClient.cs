using System.Diagnostics;
using System.Security.AccessControl;
using System.Security.Principal;
using System.Text;
using System.Text.Json;

namespace CodexSwitch.Core;

public interface ICliClient
{
    Task<UiStatus> Status();
    Task<Account> RefreshUsage(Account account);
    Task Use(Account account);
}

public sealed class CliClient(CliProcess process) : ICliClient
{
    public async Task<UiStatus> Status() => UiStatus.Decode(await process.Execute(["ui", "status", "--json"], readOnly: true));

    public async Task<Account> RefreshUsage(Account account)
    {
        if (!Selectors.IsValid(account.Alias)) throw new UiException("Rename this account with the CLI before refreshing.");
        // Refresh can reconcile credentials; it must finish its own recovery and persistence.
        var output = await process.Execute(["account", "usage", "--json", "--", account.Alias], readOnly: false);
        try
        {
            var updated = JsonSerializer.Deserialize<Account>(output, UiStatus.JsonOptions) ?? throw new JsonException();
            var usage = updated.Usage;
            var now = DateTimeOffset.UtcNow;
            if (updated.Id != account.Id || updated.Alias != account.Alias || updated.AccountId != account.AccountId ||
                updated.WorkspaceId != account.WorkspaceId || usage?.Status != "fresh" || !string.IsNullOrEmpty(usage.Error) ||
                !DateTimeOffset.TryParse(usage.FetchedAt, System.Globalization.CultureInfo.InvariantCulture,
                    System.Globalization.DateTimeStyles.RoundtripKind, out var fetched) ||
                fetched <= DateTimeOffset.UnixEpoch || fetched > now || now - fetched > TimeSpan.FromMinutes(5))
                throw new JsonException();
            return updated;
        }
        catch (Exception e) when (e is JsonException or InvalidOperationException or FormatException)
        {
            throw new UiException("Usage refresh could not be verified. Cached usage is retained.");
        }
    }

    public async Task Use(Account account)
    {
        if (!Selectors.IsValid(account.Alias)) throw new UiException("Rename this account with the CLI before switching.");
        var output = await process.Execute(["use", account.Alias, "--json"], readOnly: false);
        try
        {
            var result = JsonSerializer.Deserialize<SwitchResult>(output, UiStatus.JsonOptions);
            if (result is null || result.ProfileId != account.Id || result.Alias != account.Alias)
                throw new JsonException();
        }
        catch (Exception e) when (e is JsonException or KeyNotFoundException or InvalidOperationException)
        {
            throw new UiException("The switch result could not be verified. Reload status before trying again.");
        }
    }

    private sealed record SwitchResult
    {
        public required bool Changed { get; init; }
        public required string ProfileId { get; init; }
        public required string Alias { get; init; }
    }
}

public static class CliResolver
{
    public static string Resolve(string baseDirectory, string? configuredPath)
    {
        var candidate = configuredPath ?? Path.Combine(baseDirectory, "Helpers", "codex-switch.exe");
        if (!Path.IsPathFullyQualified(candidate) || candidate.StartsWith(@"\\", StringComparison.Ordinal) ||
            !string.Equals(Path.GetExtension(candidate), ".exe", StringComparison.OrdinalIgnoreCase))
            throw new UiException("Configure an absolute local path to codex-switch.exe.");
        try
        {
            var fullPath = Path.GetFullPath(candidate);
            var file = new FileInfo(fullPath);
            if (!file.Exists || (file.Attributes & (FileAttributes.Directory | FileAttributes.ReparsePoint)) != 0)
                throw new UiException("CLI unavailable. Keep Helpers/codex-switch.exe beside this app.");
            var user = WindowsIdentity.GetCurrent().User?.Value;
            var trusted = new HashSet<string>(StringComparer.OrdinalIgnoreCase)
            {
                user ?? "", "S-1-5-18", "S-1-5-32-544",
                // Windows owns protected system directories through TrustedInstaller.
                "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
            };
            ValidateAcl(file.GetAccessControl(), trusted, isDirectory: false);
            for (var parent = file.Directory; parent is not null; parent = parent.Parent)
            {
                if ((parent.Attributes & FileAttributes.ReparsePoint) != 0)
                    throw new UiException("The CLI path contains a link. Choose a trusted local installation.");
                ValidateAcl(parent.GetAccessControl(), trusted, isDirectory: true);
            }
            return fullPath;
        }
        catch (UiException) { throw; }
        catch { throw new UiException("The CLI path or permissions could not be verified."); }
    }

    internal static void ValidateAcl(FileSystemSecurity security, HashSet<string> trusted, bool isDirectory)
    {
        if (security.GetOwner(typeof(SecurityIdentifier)) is not SecurityIdentifier owner || !trusted.Contains(owner.Value))
            throw new UiException("The CLI path has an untrusted owner. Use a private installation.");
        var rights = FileSystemRights.Delete | FileSystemRights.ChangePermissions | FileSystemRights.TakeOwnership;
        // Creating siblings in an ancestor (for example C:\) does not permit replacing an existing protected child.
        rights |= isDirectory ? FileSystemRights.DeleteSubdirectoriesAndFiles :
            FileSystemRights.WriteData | FileSystemRights.AppendData | FileSystemRights.WriteAttributes | FileSystemRights.WriteExtendedAttributes;
        foreach (FileSystemAccessRule rule in security.GetAccessRules(true, true, typeof(SecurityIdentifier)))
        {
            if (rule.AccessControlType == AccessControlType.Allow && (rule.PropagationFlags & PropagationFlags.InheritOnly) == 0 &&
                (rule.FileSystemRights & rights) != 0 && !trusted.Contains(rule.IdentityReference.Value))
                throw new UiException("The CLI path can be modified by another user. Use a private installation.");
        }
    }
}

public sealed class CliProcess(Func<string> resolve, TimeSpan? statusTimeout = null)
{
    public async Task<string> Execute(string[] arguments, bool readOnly)
    {
        try
        {
            var start = new ProcessStartInfo(resolve())
            {
                UseShellExecute = false,
                CreateNoWindow = true,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                RedirectStandardInput = true,
                StandardOutputEncoding = Encoding.UTF8,
                StandardErrorEncoding = Encoding.UTF8
            };
            foreach (var argument in arguments) start.ArgumentList.Add(argument);
            using var child = new Process { StartInfo = start };
            if (!child.Start()) throw new UiException("The CLI could not be started.");
            child.StandardInput.Close();
            // Drain both streams concurrently and retain only bounded public stdout. Never expose stderr.
            var stdout = Capture(child.StandardOutput, 4 * 1024 * 1024);
            var stderr = Capture(child.StandardError, 0);
            var exit = child.WaitForExitAsync();
            if (readOnly && await Task.WhenAny(exit, Task.Delay(statusTimeout ?? TimeSpan.FromSeconds(5))) != exit)
            {
                try { child.Kill(entireProcessTree: true); } catch (InvalidOperationException) { }
                await exit;
                await Task.WhenAll(stdout, stderr);
                throw new UiException("Reading status timed out. Retry when the CLI is available.");
            }
            // Mutating commands finish their own persistence/recovery; the UI never cancels them.
            await exit;
            var result = await stdout;
            await stderr;
            if (child.ExitCode != 0) throw new UiException("The CLI command failed. Reload status and review account state with the CLI.");
            if (result.Exceeded) throw new UiException("The CLI response exceeded the supported size.");
            return result.Text;
        }
        catch (UiException) { throw; }
        catch { throw new UiException("The CLI could not complete the command. Check the installation and retry."); }
    }

    private static async Task<(string Text, bool Exceeded)> Capture(StreamReader reader, int limit)
    {
        var text = new StringBuilder();
        var buffer = new char[4096];
        var exceeded = false;
        int count;
        while ((count = await reader.ReadAsync(buffer)) != 0)
        {
            if (limit == 0) continue;
            if (count > limit - text.Length) { exceeded = true; continue; }
            if (!exceeded) text.Append(buffer, 0, count);
        }
        return (text.ToString(), exceeded);
    }
}
