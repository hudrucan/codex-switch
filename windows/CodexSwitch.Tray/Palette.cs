using System;
using System.Windows;
using System.Windows.Media;
using Microsoft.Win32;

namespace CodexSwitch.Tray;

internal static class Palette
{
    public static void Apply(bool? darkOverride = null)
    {
        var dark = false;
        try { dark = Registry.GetValue(@"HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize", "AppsUseLightTheme", 1) is int value && value == 0; }
        catch { /* System colors remain available when theme preferences cannot be read. */ }
        dark = darkOverride ?? dark;
        var resources = Application.Current.Resources;
        if (SystemParameters.HighContrast)
        {
            resources["Page"] = SystemColors.WindowBrush;
            resources["Card"] = SystemColors.WindowBrush;
            resources["Ink"] = SystemColors.WindowTextBrush;
            resources["Muted"] = SystemColors.WindowTextBrush;
            resources["Line"] = SystemColors.WindowTextBrush;
            resources["Accent"] = SystemColors.HighlightBrush;
            resources["Hover"] = SystemColors.ControlBrush;
            return;
        }
        Set("Page", dark ? "#252527" : "#F7F7F8");
        Set("Card", dark ? "#252527" : "#F7F7F8");
        Set("Ink", dark ? "#F1F1F3" : "#242426");
        Set("Muted", dark ? "#AEADB2" : "#707076");
        Set("Line", dark ? "#414144" : "#DDDDDF");
        Set("Accent", dark ? "#87ACF0" : "#296BCF");
        Set("Hover", dark ? "#343437" : "#E9E9EC");

        void Set(string key, string color) => resources[key] = new SolidColorBrush((Color)ColorConverter.ConvertFromString(color));
    }
}
