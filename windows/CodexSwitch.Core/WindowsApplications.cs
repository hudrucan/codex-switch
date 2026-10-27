using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Security.Principal;
using System.Text;
using Microsoft.Win32.SafeHandles;

namespace CodexSwitch.Core;

public sealed record ApplicationTarget(int Pid, long StartedAt, string Executable, nint Window);

public interface IApplicationControl
{
    ApplicationTarget? Identify(CodexProcess process);
    bool RequestClose(ApplicationTarget target);
}

internal sealed record DesktopProcess(int Pid, int ParentPid, long StartedAt, string Executable,
    string PackageFamily, nint Window, bool IsCurrentUserSession);

internal static class ApplicationOwnership
{
    internal const string SupportedPackage = "OpenAI.Codex_2p2nqsd0c76g0";

    internal static ApplicationTarget? Identify(CodexProcess reported, Func<int, DesktopProcess?> read, string sidecarDirectory)
    {
        if (reported.Pid <= 0 || !IsCodexName(reported.Command)) return null;
        var lineage = new List<DesktopProcess>();
        var seen = new HashSet<int>();
        var pid = reported.Pid;
        while (pid > 0 && lineage.Count < 32 && seen.Add(pid))
        {
            var node = read(pid);
            if (node is null || node.Pid != pid || !node.IsCurrentUserSession || node.StartedAt <= 0 || !Path.IsPathFullyQualified(node.Executable)) return null;
            lineage.Add(node);
            if (node.Window != 0 && node.PackageFamily == SupportedPackage && IsCodexName(Path.GetFileName(node.Executable)))
            {
                var directory = Path.GetDirectoryName(node.Executable);
                if (directory is null || !lineage.All(child => child.StartedAt >= node.StartedAt &&
                    (child.PackageFamily == SupportedPackage || child.PackageFamily.Length == 0 &&
                        (Within(child.Executable, directory) || Within(child.Executable, sidecarDirectory) && IsCodexName(Path.GetFileName(child.Executable))))) ||
                    !lineage.All(child => read(child.Pid) == child)) return null;
                return new ApplicationTarget(node.Pid, node.StartedAt, node.Executable, node.Window);
            }
            pid = node.ParentPid;
        }
        return null;
    }

    private static bool IsCodexName(string name) => name.Equals("codex.exe", StringComparison.OrdinalIgnoreCase) || name.Equals("chatgpt.exe", StringComparison.OrdinalIgnoreCase);
    private static bool Within(string path, string directory) => Path.IsPathFullyQualified(path) && path.StartsWith(directory.TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase);
}

public sealed class WindowsApplications : IApplicationControl
{
    private readonly string sidecars = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "OpenAI", "Codex", "bin");

    public ApplicationTarget? Identify(CodexProcess process) => ApplicationOwnership.Identify(process, Read, sidecars);

    public bool RequestClose(ApplicationTarget target)
    {
        try
        {
            using var process = Process.GetProcessById(target.Pid);
            // Keep the process handle open while rechecking identity and posting WM_CLOSE.
            _ = process.SafeHandle;
            if (Identify(new CodexProcess { Pid = target.Pid, Command = Path.GetFileName(target.Executable) }) != target) return false;
            return process.CloseMainWindow();
        }
        catch { return false; }
    }

    private static DesktopProcess? Read(int pid)
    {
        try
        {
            using var process = Process.GetProcessById(pid);
            using var currentProcess = Process.GetCurrentProcess();
            if (process.HasExited || process.SessionId != currentProcess.SessionId ||
                !OpenProcessToken(process.SafeHandle, 8, out var token)) return null;
            using (token)
            using (var identity = new WindowsIdentity(token.DangerousGetHandle()))
            using (var current = WindowsIdentity.GetCurrent())
            {
                if (identity.User != current.User) return null;
            }
            var executable = process.MainModule?.FileName;
            if (executable is null) return null;
            uint length = 0;
            var result = GetPackageFamilyName(process.SafeHandle, ref length, null);
            var package = "";
            if (result == 122 && length is > 0 and <= 1024)
            {
                var name = new StringBuilder((int)length);
                if (GetPackageFamilyName(process.SafeHandle, ref length, name) != 0) return null;
                package = name.ToString();
            }
            else if (result != 15700) return null; // APPMODEL_ERROR_NO_PACKAGE
            var window = process.MainWindowHandle;
            if (window != 0 && (GetWindowThreadProcessId(window, out var windowPid) == 0 || windowPid != pid)) return null;
            var parent = ParentPid(pid);
            return parent is null ? null : new DesktopProcess(pid, parent.Value, process.StartTime.ToUniversalTime().Ticks,
                Path.GetFullPath(executable), package, window, true);
        }
        catch { return null; }
    }

    private static int? ParentPid(int pid)
    {
        using var snapshot = CreateToolhelp32Snapshot(2, 0); // TH32CS_SNAPPROCESS
        if (snapshot.IsInvalid) return null;
        var entry = new ProcessEntry { Size = (uint)Marshal.SizeOf<ProcessEntry>(), Executable = "" };
        if (!Process32First(snapshot, ref entry)) return null;
        do { if (entry.Pid == pid) return checked((int)entry.ParentPid); }
        while (Process32Next(snapshot, ref entry));
        return null;
    }

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    private struct ProcessEntry
    {
        public uint Size, Usage, Pid;
        public nuint Heap;
        public uint Module, Threads, ParentPid;
        public int Priority;
        public uint Flags;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 260)] public string Executable;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, ExactSpelling = true)] private static extern int GetPackageFamilyName(SafeProcessHandle process, ref uint length, StringBuilder? family);
    [DllImport("advapi32.dll", SetLastError = true)][return: MarshalAs(UnmanagedType.Bool)] private static extern bool OpenProcessToken(SafeProcessHandle process, uint access, out SafeAccessTokenHandle token);
    [DllImport("kernel32.dll", SetLastError = true)] private static extern SafeFileHandle CreateToolhelp32Snapshot(uint flags, uint pid);
    [DllImport("kernel32.dll", EntryPoint = "Process32FirstW", CharSet = CharSet.Unicode)][return: MarshalAs(UnmanagedType.Bool)] private static extern bool Process32First(SafeFileHandle snapshot, ref ProcessEntry entry);
    [DllImport("kernel32.dll", EntryPoint = "Process32NextW", CharSet = CharSet.Unicode)][return: MarshalAs(UnmanagedType.Bool)] private static extern bool Process32Next(SafeFileHandle snapshot, ref ProcessEntry entry);
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(nint window, out uint pid);
}
