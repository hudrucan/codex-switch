namespace CodexSwitch.Core;

public sealed class SwitchController(ICliClient client, Func<DateTimeOffset>? clock = null)
{
    public UiStatus? Status { get; private set; }
    public bool IsBusy { get; private set; }
    public bool StatusAvailable { get; private set; }
    public string? Message { get; private set; }
    public Account? PendingAccount { get; private set; }
    public bool IsRefreshing { get; private set; }
    public int RefreshCompleted { get; private set; }
    public int RefreshTotal { get; private set; }
    public string? RefreshingAlias { get; private set; }
    public string? RefreshNotice { get; private set; }
    public event Action? RefreshProgressChanged;
    private DateTimeOffset nextRefreshAt;
    private int refreshFailures;
    private DateTimeOffset Now => clock?.Invoke() ?? DateTimeOffset.UtcNow;

    public async Task RefreshUsage()
    {
        if (IsBusy) return;
        var now = Now;
        if (now < nextRefreshAt)
        {
            RefreshNotice = $"Refresh available in {Math.Ceiling((nextRefreshAt - now).TotalSeconds):0} seconds.";
            return;
        }
        IsBusy = true;
        IsRefreshing = true;
        Message = null;
        PendingAccount = null;
        RefreshNotice = null;
        RefreshCompleted = 0;
        RefreshTotal = 0;
        var succeeded = 0;
        var failures = 0;
        try
        {
            if (!await ReadStatus()) { failures++; return; }
            var snapshot = Status!;
            RefreshTotal = snapshot.Accounts.Length;
            foreach (var account in snapshot.Accounts)
            {
                RefreshingAlias = account.Alias;
                RefreshProgressChanged?.Invoke();
                try
                {
                    if (!Selectors.IsUnambiguous(account, snapshot)) throw new UiException("Rename the ambiguous alias with the CLI before refreshing.");
                    var updated = await client.RefreshUsage(account);
                    // Only usage is replaced; a response never changes the observed active identity.
                    Status = Status! with { Accounts = Status.Accounts.Select(a => a.Id == account.Id ? a with { Usage = updated.Usage } : a).ToArray() };
                    succeeded++;
                }
                catch { failures++; }
                RefreshCompleted++;
                RefreshProgressChanged?.Invoke();
            }
            RefreshingAlias = null;
            // Re-observe account identity/processes after the CLI finishes all reconciliation.
            // ReadStatus retains completed updates if this final read fails.
            if (!await ReadStatus()) failures++;
            RefreshNotice = $"Updated {succeeded} of {RefreshTotal} accounts.";
            if (failures > 0) Message = "Some usage updates could not be verified. Completed updates and cached usage are retained.";
        }
        finally
        {
            refreshFailures = failures == 0 ? 0 : Math.Min(refreshFailures + 1, 3);
            var delay = refreshFailures switch { 2 => 120, 3 => 300, _ => 60 };
            nextRefreshAt = Now.AddSeconds(delay);
            IsRefreshing = false;
            RefreshingAlias = null;
            IsBusy = false;
        }
    }

    public async Task Reload()
    {
        if (IsBusy) return;
        IsBusy = true;
        Message = null;
        PendingAccount = null;
        try { await ReadStatus(); }
        finally { IsBusy = false; }
    }

    public async Task Switch(Account selected)
    {
        if (IsBusy) return;
        IsBusy = true;
        Message = null;
        PendingAccount = null;
        try
        {
            if (!await ReadStatus()) return;
            var current = Status!.Accounts.SingleOrDefault(a => a.Id == selected.Id && a.Alias == selected.Alias &&
                a.AccountId == selected.AccountId && a.WorkspaceId == selected.WorkspaceId);
            if (current is null || !Selectors.IsUnambiguous(current, Status))
            {
                Message = "This account changed or has an ambiguous alias. Reload or rename it with the CLI.";
                return;
            }
            if (!Status.CanSwitch)
            {
                Message = Status.Notice;
                return;
            }
            if (Status.ActiveId == current.Id) { Message = "This account is already active."; return; }
            if (Status.CodexProcesses.Length > 0)
            {
                PendingAccount = current;
                Message = "Close Codex and any Codex terminals, then select Retry Switch. No applications will be closed for you.";
                return;
            }
            try
            {
                await client.Use(current);
                if (await ReadStatus())
                    Message = Status!.ActiveId == current.Id
                        ? "Account switched. Open Codex when you are ready."
                        : "The selected account is not observed as active. Review account state with the CLI.";
            }
            catch
            {
                // The CLI remains authoritative if Codex starts between the snapshot and guarded use.
                if (await ReadStatus() && Status!.CodexProcesses.Length > 0)
                {
                    PendingAccount = current;
                    Message = "Codex is running. Close it and select Retry Switch.";
                }
                else Message = "The switch could not be verified. Reload status and review the CLI before retrying.";
            }
        }
        finally { IsBusy = false; }
    }

    private async Task<bool> ReadStatus()
    {
        try
        {
            Status = await client.Status();
            StatusAvailable = true;
            return true;
        }
        catch
        {
            StatusAvailable = false;
            Message = "Status unavailable. Previous cached data is retained. Check the CLI installation and retry.";
            return false;
        }
    }
}
