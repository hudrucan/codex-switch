using System;
using System.IO;
using System.Security.Principal;
using System.Threading;
using System.Windows;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using CodexSwitch.Core;
using Forms = System.Windows.Forms;

namespace CodexSwitch.Tray;

public partial class App : Application
{
    private Mutex? instance;
    private Forms.NotifyIcon? tray;
    private MainWindow? popup;
    private System.Drawing.Icon? icon;

    protected override async void OnStartup(StartupEventArgs e)
    {
        base.OnStartup(e);
        var preview = e.Args.Length is >= 2 and <= 4 && e.Args[0] == "--preview";
        var demo = e.Args.Length == 1 && e.Args[0] == "--demo";
        if (e.Args.Length != 0 && !preview && !demo) { Shutdown(2); return; }
        var scenario = preview && e.Args.Length >= 3 ? e.Args[2] : "active";
        var theme = preview && e.Args.Length == 4 ? e.Args[3] : null;
        if (scenario is not ("active" or "unmanaged" or "many") || theme is not (null or "light" or "dark")) { Shutdown(2); return; }
        Palette.Apply(theme is null ? null : theme == "dark");
        if (preview || demo)
        {
            popup = new MainWindow(new SwitchController(new DemoClient(scenario)), isDemo: true);
            if (preview)
            {
                await popup.LoadStatus();
                var content = (FrameworkElement)popup.Content;
                content.Measure(new Size(400, double.PositiveInfinity));
                content.Arrange(new Rect(0, 0, 400, content.DesiredSize.Height));
                content.UpdateLayout();
                var bitmap = new RenderTargetBitmap(400, (int)Math.Ceiling(content.ActualHeight), 96, 96, PixelFormats.Pbgra32);
                bitmap.Render(content);
                var encoder = new PngBitmapEncoder();
                encoder.Frames.Add(BitmapFrame.Create(bitmap));
                // Preview is entirely synthetic and never constructs the real CLI client.
                using (var output = File.Create(Path.GetFullPath(e.Args[1]))) encoder.Save(output);
                Shutdown();
                return;
            }
        }
        else
        {
            var sid = WindowsIdentity.GetCurrent().User?.Value ?? Environment.UserName;
            instance = new Mutex(true, @"Local\CodexSwitch.Tray." + sid, out var created);
            if (!created) { Shutdown(); return; }
            var process = new CliProcess(() => CliResolver.Resolve(AppContext.BaseDirectory,
                Environment.GetEnvironmentVariable("CODEX_SWITCH_CLI_PATH")));
            popup = new MainWindow(new SwitchController(new CliClient(process)), isDemo: false);
        }
        icon = TrayIcon.Create();
        var menu = new Forms.ContextMenuStrip();
        menu.Items.Add("Open Codex Switch", null, (_, _) => ShowPopup());
        var quit = menu.Items.Add("Exit", null, (_, _) => RequestExit());
        menu.Opening += (_, _) => quit.Enabled = !popup.IsBusy;
        tray = new Forms.NotifyIcon { Icon = icon, Text = demo ? "Codex Switch · Demo" : "Codex Switch", Visible = true, ContextMenuStrip = menu };
        tray.MouseUp += (_, args) => { if (args.Button == Forms.MouseButtons.Left) ShowPopup(); };
        if (demo) ShowPopup();
    }

    private void ShowPopup()
    {
        if (popup is null) return;
        if (popup.IsVisible) { popup.Activate(); return; }
        Palette.Apply();
        popup.ShowAtCursor();
        _ = popup.LoadStatus();
    }

    internal void RequestExit()
    {
        if (popup?.IsBusy == true) return;
        popup?.CloseForExit();
        Shutdown();
    }

    protected override void OnExit(ExitEventArgs e)
    {
        tray?.Dispose();
        icon?.Dispose();
        instance?.Dispose();
        base.OnExit(e);
    }
}
