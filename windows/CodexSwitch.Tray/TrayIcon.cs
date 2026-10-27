using System;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Windows.Media;
using System.Windows.Media.Imaging;
using System.Xml.Linq;
using Drawing = System.Drawing;
using Forms = System.Windows.Forms;

namespace CodexSwitch.Tray;

internal static class TrayIcon
{
    [DllImport("user32.dll")] private static extern bool DestroyIcon(nint icon);

    private static readonly (Geometry Rear, Geometry Front, Geometry Glyph) Mark = LoadMaster();

    public static Drawing.Icon Create()
    {
        var pixels = Math.Clamp(Forms.SystemInformation.SmallIconSize.Width, 16, 64);
        using var stream = new MemoryStream();
        var encoder = new PngBitmapEncoder();
        encoder.Frames.Add(BitmapFrame.Create(Render(pixels)));
        encoder.Save(stream);
        stream.Position = 0;
        using var bitmap = new Drawing.Bitmap(stream);
        var handle = bitmap.GetHicon();
        try { using var borrowed = Drawing.Icon.FromHandle(handle); return (Drawing.Icon)borrowed.Clone(); }
        finally { DestroyIcon(handle); }
    }

    internal static BitmapSource Render(int pixels)
    {
        // Use the original silhouette, filling the tray slot with a Windows-weight outline.
        var stroke = new Pen(Brushes.White, 2.4) { StartLineCap = PenLineCap.Round, EndLineCap = PenLineCap.Round, LineJoin = PenLineJoin.Round };
        var gap = new Pen(Brushes.Black, 4.7) { LineJoin = PenLineJoin.Round };
        var frontArea = Geometry.Combine(Mark.Front, Mark.Front.GetWidenedPathGeometry(gap), GeometryCombineMode.Union, null);
        var rearClip = Geometry.Combine(new RectangleGeometry(new System.Windows.Rect(0, 0, 24, 24)), frontArea, GeometryCombineMode.Exclude, null);
        var visual = new DrawingVisual();
        using (var drawing = visual.RenderOpen())
        {
            drawing.PushTransform(new ScaleTransform(pixels / 24d, pixels / 24d));
            drawing.PushClip(rearClip);
            drawing.DrawGeometry(null, stroke, Mark.Rear);
            drawing.Pop();
            drawing.DrawGeometry(null, stroke, Mark.Front);
            drawing.DrawGeometry(null, stroke, Mark.Glyph);
            drawing.Pop();
        }
        var result = new RenderTargetBitmap(pixels, pixels, 96, 96, PixelFormats.Pbgra32);
        result.Render(visual);
        result.Freeze();
        return result;
    }

    private static (Geometry, Geometry, Geometry) LoadMaster()
    {
        using var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream("CodexSwitch.TrayOutline.svg")
            ?? throw new InvalidOperationException("The shared tray icon is missing.");
        XNamespace svg = "http://www.w3.org/2000/svg";
        var paths = XDocument.Load(stream).Descendants(svg + "mask").Single().Descendants(svg + "path")
            .Select(path => Geometry.Parse((string?)path.Attribute("d") ?? throw new InvalidOperationException("The shared icon path is invalid."))).ToArray();
        foreach (var path in paths) path.Freeze();
        return (paths[0], paths[2], paths[3]);
    }
}
