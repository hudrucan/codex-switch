using System;
using System.ComponentModel;
using System.Diagnostics;
using System.IO;
using System.Globalization;
using System.Linq;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Interop;
using System.Windows.Media;
using CodexSwitch.Core;
using Microsoft.Win32;
using Forms = System.Windows.Forms;

namespace CodexSwitch.Tray;

public partial class MainWindow : Window
{
    private readonly SwitchController controller;
    private readonly bool isDemo;
    private bool exiting;
    private System.Drawing.Point anchor;
    public bool IsBusy => controller.IsBusy;

    public MainWindow(SwitchController controller, bool isDemo)
    {
        this.controller = controller;
        this.isDemo = isDemo;
        InitializeComponent();
        controller.RefreshProgressChanged += Render;
        Deactivated += (_, _) => Hide();
        PreviewKeyDown += (_, e) => { if (e.Key == Key.Escape) { Hide(); e.Handled = true; } };
        Closing += OnClosing;
    }

    public Task LoadStatus() => Run(controller.Reload);
    public Task SelectAccount(Account account) => Run(() => controller.Switch(account));
    public void CloseForExit() { exiting = true; Close(); }

    private void OnClosing(object? sender, CancelEventArgs args)
    {
        if (!exiting) { args.Cancel = true; Hide(); }
    }

    public void ShowAtCursor()
    {
        anchor = Forms.Control.MousePosition;
        Show();
        PositionAtCursor();
        Activate();
    }

    private void PositionAtCursor()
    {
        var cursor = anchor;
        var area = Forms.Screen.FromPoint(cursor).WorkingArea;
        var transform = PresentationSource.FromVisual(this)?.CompositionTarget?.TransformFromDevice ?? Matrix.Identity;
        var point = transform.Transform(new Point(cursor.X, cursor.Y));
        var topLeft = transform.Transform(new Point(area.Left, area.Top));
        var bottomRight = transform.Transform(new Point(area.Right, area.Bottom));
        Width = Math.Min(400, Math.Max(280, bottomRight.X - topLeft.X - 16));
        MaxHeight = Math.Max(240, bottomRight.Y - topLeft.Y - 16);
        UpdateLayout();
        Left = Math.Clamp(point.X - ActualWidth + 24, topLeft.X + 8, Math.Max(topLeft.X + 8, bottomRight.X - ActualWidth - 8));
        var y = point.Y > (topLeft.Y + bottomRight.Y) / 2 ? point.Y - ActualHeight - 12 : point.Y + 12;
        Top = Math.Clamp(y, topLeft.Y + 8, Math.Max(topLeft.Y + 8, bottomRight.Y - ActualHeight - 8));
    }

    private async Task Run(Func<Task> operation)
    {
        var task = operation();
        Render();
        string? error = null;
        try { await task; }
        catch { error = "The operation could not complete. Reload status and retry."; }
        finally
        {
            Render();
            if (error is not null) ShowNotice(error);
            if (IsVisible) PositionAtCursor();
        }
    }

    private void Render()
    {
        CurrentPanel.Children.Clear();
        SavedPanel.Children.Clear();
        var status = controller.Status;
        SavedSection.Visibility = Visibility.Visible;
        SavedTitle.Text = "SAVED ACCOUNTS";
        Subtitle.Text = isDemo ? "Synthetic preview" : status is null ? "" : $"{status.Accounts.Length} accounts";
        var confirming = controller.PendingAccount is not null;
        Actions.IsEnabled = !IsBusy && !confirming;
        Actions.Opacity = confirming ? 0.5 : 1;
        Quit.IsEnabled = !IsBusy && !confirming;
        Refresh.Content = controller.IsRefreshing ? $"↻   Refreshing {controller.RefreshCompleted}/{controller.RefreshTotal}…" : "↻   Refresh Usage";
        RefreshFeedback.Text = controller.IsRefreshing
            ? controller.RefreshingAlias is null ? "Reloading account status…" : $"Refreshing {controller.RefreshingAlias} · completed {controller.RefreshCompleted}/{controller.RefreshTotal}"
            : controller.RefreshNotice ?? "Usage is cached. Refresh updates all saved accounts.";
        Activity.Text = controller.IsClosing ? "Waiting for Codex to close…" : IsBusy ? "Working…" : !controller.StatusAvailable ? "Process status unavailable" :
            status!.CodexProcesses.Length == 0 ? "●  Codex is closed" : "●  Codex is running";
        SwitchConfirmation.Visibility = confirming ? Visibility.Visible : Visibility.Collapsed;
        SwitchConfirmation.IsEnabled = !IsBusy;
        ConfirmationTitle.Text = $"Switch to {controller.PendingAccount?.Alias}";
        ProcessCount.Text = controller.StatusAvailable ? $"{status!.CodexProcesses.Length} running process{(status.CodexProcesses.Length == 1 ? "" : "es")}" : "Process status unavailable";
        ConfirmationNotice.Text = controller.Message ?? "";
        RetrySwitch.Visibility = confirming && !controller.CanCloseAndSwitch ? Visibility.Visible : Visibility.Collapsed;
        CloseSwitch.Visibility = controller.CanCloseAndSwitch ? Visibility.Visible : Visibility.Collapsed;
        var active = controller.StatusAvailable ? status?.Accounts.FirstOrDefault(a => a.Id == status.ActiveId) : null;
        if (active is not null)
        {
            CurrentPanel.Children.Add(Section("CURRENT ACCOUNT"));
            CurrentPanel.Children.Add(ActiveAccount(active));
        }
        if (status is null || status.Accounts.Length == 0)
        {
            SavedPanel.Children.Add(Label(status is null ? "Open the menu to read account status." :
                "No accounts saved. Add an account with the CLI.", muted: true));
        }
        else
        {
            var others = status.Accounts.Where(a => a.Id != active?.Id).ToArray();
            SavedTitle.Text = active is null ? "SAVED ACCOUNTS" : "SWITCH ACCOUNT";
            SavedSection.Visibility = others.Length == 0 ? Visibility.Collapsed : Visibility.Visible;
            foreach (var account in others)
            {
                var button = new Button
                {
                    Content = SavedAccount(account),
                    Padding = new Thickness(10),
                    Margin = new Thickness(0, 0, 0, 2),
                    HorizontalContentAlignment = HorizontalAlignment.Stretch,
                    IsEnabled = !IsBusy && !confirming && controller.StatusAvailable && status.CanSwitch && Selectors.IsUnambiguous(account, status)
                };
                AutomationProperties.SetName(button, $"Switch to {account.Alias}");
                button.Click += async (_, _) => await SelectAccount(account);
                SavedPanel.Children.Add(button);
            }
        }
        var notice = controller.Message ?? status?.Notice;
        if (notice is null && status?.CodexProcesses.Length > 0)
            notice = "Codex is running. Close it before switching accounts.";
        ShowNotice(confirming ? null : notice);
        Open.IsEnabled = !IsBusy && !isDemo;
    }

    private FrameworkElement ActiveAccount(Account account)
    {
        var content = new StackPanel { Margin = new Thickness(0, 0, 0, 16) };
        var title = new DockPanel();
        var mark = Label("✓");
        mark.FontSize = 16;
        mark.SetResourceReference(TextBlock.ForegroundProperty, "Accent");
        mark.HorizontalAlignment = HorizontalAlignment.Right;
        DockPanel.SetDock(mark, Dock.Right);
        title.Children.Add(mark);
        title.Children.Add(AccountTitle(account, 21));
        content.Children.Add(title);
        var limits = account.Usage?.IsAvailable == true ? account.Usage.RateLimits?.Main : null;
        var quotas = new Grid { Margin = new Thickness(0, 16, 0, 10) };
        quotas.ColumnDefinitions.Add(new ColumnDefinition());
        quotas.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(24) });
        quotas.ColumnDefinitions.Add(new ColumnDefinition());
        var primary = QuotaMeter(limits?.Primary, "Primary");
        var secondary = QuotaMeter(limits?.Secondary, "Secondary");
        Grid.SetColumn(secondary, 2);
        quotas.Children.Add(primary);
        quotas.Children.Add(secondary);
        content.Children.Add(quotas);
        var credits = Label("Banked resets   " + ResetCount(account), muted: true);
        credits.FontSize = 11;
        content.Children.Add(credits);
        var cache = CacheLabel(account);
        cache.Margin = new Thickness(0, 10, 0, 0);
        content.Children.Add(cache);
        return content;
    }

    private static FrameworkElement SavedAccount(Account account)
    {
        var content = new StackPanel();
        var heading = new DockPanel { Margin = new Thickness(0, 0, 0, 8) };
        var initial = Label(account.Alias[..1].ToUpperInvariant(), muted: true);
        initial.HorizontalAlignment = HorizontalAlignment.Center;
        initial.VerticalAlignment = VerticalAlignment.Center;
        initial.FontSize = 11;
        var avatar = new Border { Width = 28, Height = 28, CornerRadius = new CornerRadius(7), Child = initial, Margin = new Thickness(0, 0, 10, 0) };
        avatar.SetResourceReference(Border.BackgroundProperty, "Hover");
        heading.Children.Add(avatar);
        var chevron = Label("›", muted: true);
        chevron.HorizontalAlignment = HorizontalAlignment.Right;
        chevron.VerticalAlignment = VerticalAlignment.Center;
        DockPanel.SetDock(chevron, Dock.Right);
        heading.Children.Add(chevron);
        heading.Children.Add(AccountTitle(account, 13));
        content.Children.Add(heading);
        var limits = account.Usage?.IsAvailable == true ? account.Usage.RateLimits?.Main : null;
        content.Children.Add(SavedQuota(limits?.Primary, "Primary"));
        content.Children.Add(SavedQuota(limits?.Secondary, "Secondary"));
        var credits = Label("Banked resets · " + ResetCount(account), muted: true);
        credits.FontSize = 11;
        credits.Margin = new Thickness(0, 0, 0, 5);
        content.Children.Add(credits);
        content.Children.Add(CacheLabel(account));
        return content;
    }

    private static FrameworkElement AccountTitle(Account account, int size)
    {
        var title = new DockPanel { LastChildFill = true, VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Left };
        var alias = Label(account.Alias);
        alias.FontSize = size;
        alias.FontWeight = FontWeights.Medium;
        alias.VerticalAlignment = VerticalAlignment.Center;
        alias.TextTrimming = TextTrimming.CharacterEllipsis;
        alias.TextWrapping = TextWrapping.NoWrap;
        alias.ToolTip = account.Alias;
        if (account.Usage is { IsAvailable: true, PlanType: { Length: > 0 } plan })
        {
            var planLabel = Label(plan, muted: true);
            planLabel.FontSize = size == 21 ? 11 : 10;
            planLabel.Margin = new Thickness(8, 0, 0, 0);
            planLabel.VerticalAlignment = VerticalAlignment.Center;
            // Different font sizes have different ink centers even when their line boxes align.
            planLabel.RenderTransform = new TranslateTransform(0, InkOffset(alias) - InkOffset(planLabel));
            DockPanel.SetDock(planLabel, Dock.Right);
            title.Children.Add(planLabel);
        }
        title.Children.Add(alias);
        return title;
    }

    private static double InkOffset(TextBlock label)
    {
        var text = new FormattedText(label.Text, CultureInfo.CurrentUICulture, label.FlowDirection,
            new Typeface(label.FontFamily, label.FontStyle, label.FontWeight, label.FontStretch), label.FontSize, Brushes.Black, 1);
        var ink = text.BuildGeometry(new Point()).Bounds;
        return ink.IsEmpty ? 0 : ink.Top + ink.Height / 2 - text.Height / 2;
    }

    private static string ResetCount(Account account)
    {
        var resets = account.Usage?.IsAvailable == true ? account.Usage.RateLimits?.RateLimitResetCredits?.AvailableCount : null;
        return resets is >= 0 ? resets.Value.ToString() : "Unavailable";
    }

    private static TextBlock CacheLabel(Account account)
    {
        var cache = Label(account.Usage?.CacheLabel(DateTimeOffset.Now) ?? "Cached usage unavailable", muted: true);
        cache.FontSize = 10;
        return cache;
    }

    private static FrameworkElement QuotaMeter(QuotaWindow? quota, string fallback)
    {
        var panel = new StackPanel();
        var heading = new DockPanel { Margin = new Thickness(0, 0, 0, 7) };
        var remaining = Label(quota?.Remaining is int value ? $"{value}%" : "—");
        remaining.FontWeight = FontWeights.Medium;
        remaining.FontSize = 18;
        remaining.HorizontalAlignment = HorizontalAlignment.Right;
        DockPanel.SetDock(remaining, Dock.Right);
        heading.Children.Add(remaining);
        var name = Label((quota?.Title(fallback) ?? fallback).Replace(" quota", ""), muted: true);
        name.FontSize = 11;
        name.VerticalAlignment = VerticalAlignment.Center;
        heading.Children.Add(name);
        panel.Children.Add(heading);
        var bar = new ProgressBar { Minimum = 0, Maximum = 100, Value = quota?.Remaining ?? 0, Height = 3, BorderThickness = new Thickness(0) };
        bar.SetResourceReference(Control.ForegroundProperty, "Accent");
        bar.SetResourceReference(Control.BackgroundProperty, "Line");
        AutomationProperties.SetName(bar, name.Text + " remaining, cached");
        panel.Children.Add(bar);
        var reset = Label(quota?.ResetLabel ?? "Reset unavailable", muted: true);
        reset.FontSize = 10;
        reset.Margin = new Thickness(0, 7, 0, 0);
        panel.Children.Add(reset);
        return panel;
    }

    private static TextBlock SavedQuota(QuotaWindow? quota, string fallback)
    {
        var title = (quota?.Title(fallback) ?? fallback).Replace(" quota", "");
        var value = quota?.Remaining is int remaining ? $"{remaining}% left" : "Unavailable";
        var label = Label($"{title} · {value} · {quota?.ResetLabel ?? "Reset unavailable"}", muted: true);
        label.FontSize = 11;
        label.Margin = new Thickness(0, 0, 0, 5);
        return label;
    }

    private static TextBlock Section(string text)
    {
        var label = Label(text, muted: true);
        label.FontSize = 10;
        label.FontWeight = FontWeights.SemiBold;
        label.Margin = new Thickness(0, 0, 0, 9);
        return label;
    }

    private static TextBlock Label(string text, bool muted = false)
    {
        var label = new TextBlock { Text = text };
        label.SetResourceReference(TextBlock.ForegroundProperty, muted ? "Muted" : "Ink");
        return label;
    }

    private void ShowNotice(string? text)
    {
        Notice.Text = text ?? "";
        NoticeBox.Visibility = string.IsNullOrEmpty(text) ? Visibility.Collapsed : Visibility.Visible;
    }

    private async void RefreshClick(object sender, RoutedEventArgs e) => await Run(controller.RefreshUsage);
    private async void CloseSwitchClick(object sender, RoutedEventArgs e) => await Run(controller.CloseAndSwitch);
    private void CancelClick(object sender, RoutedEventArgs e) { controller.CancelSwitch(); Render(); if (IsVisible) PositionAtCursor(); }
    private void QuitClick(object sender, RoutedEventArgs e) => ((App)Application.Current).RequestExit();
    private async void RetrySwitchClick(object sender, RoutedEventArgs e)
    {
        if (controller.PendingAccount is { } selected) await Run(() => controller.Switch(selected));
    }

    private void OpenClick(object sender, RoutedEventArgs e)
    {
        if (IsBusy || isDemo) return;
        try
        {
            // ShellExecute activates only a registered protocol or a known Start-menu shortcut.
            foreach (var protocol in new[] { "codex", "chatgpt" })
            {
                using var registration = Registry.ClassesRoot.OpenSubKey(protocol);
                if (registration?.GetValue("URL Protocol") is not null)
                {
                    Process.Start(new ProcessStartInfo(protocol + "://") { UseShellExecute = true });
                    Hide();
                    return;
                }
            }
            foreach (var folder in new[] { Environment.SpecialFolder.StartMenu, Environment.SpecialFolder.CommonStartMenu })
                foreach (var name in new[] { "Codex.lnk", "ChatGPT.lnk", Path.Combine("OpenAI", "Codex.lnk"), Path.Combine("OpenAI", "ChatGPT.lnk") })
                {
                    var shortcut = Path.Combine(Environment.GetFolderPath(folder), "Programs", name);
                    if (!File.Exists(shortcut)) continue;
                    Process.Start(new ProcessStartInfo(shortcut) { UseShellExecute = true });
                    Hide();
                    return;
                }
            ShowNotice("No registered Codex launcher was found. Open ChatGPT or Codex from the Start menu.");
        }
        catch { ShowNotice("Codex could not be opened. Open ChatGPT or Codex from the Start menu."); }
    }
}
