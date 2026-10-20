import AppKit
import Foundation

// Compile with Sources/CodexSwitchApp/MenuBarIcon.swift to reuse the approved mark.
@main @MainActor
struct RenderAppIcon {
  static func main() throws {
    guard CommandLine.arguments.count == 2 else {
      fatalError("Usage: render-app-icon output.iconset")
    }
    let output = URL(fileURLWithPath: CommandLine.arguments[1])
    try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
    MenuBarIcon.image.cacheMode = .never
    let mark = NSImage(size: NSSize(width: 18, height: 18), flipped: true) { rect in
      MenuBarIcon.image.draw(in: rect)
      NSColor.white.setFill()
      rect.fill(using: .sourceAtop)
      return true
    }
    mark.cacheMode = .never
    for size in [16, 32, 128, 256, 512] {
      for scale in [1, 2] {
        let pixels = size * scale
        let bitmap = NSBitmapImageRep(
          bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
          bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
          colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
        let transform = NSAffineTransform()
        transform.scale(by: CGFloat(pixels) / 1024)
        transform.concat()
        let tile = NSBezierPath(
          roundedRect: NSRect(x: 90, y: 90, width: 844, height: 844),
          xRadius: 190, yRadius: 190)
        NSGraphicsContext.saveGraphicsState()
        let shadow = NSShadow()
        shadow.shadowColor = NSColor.black.withAlphaComponent(0.18)
        shadow.shadowBlurRadius = 22
        shadow.shadowOffset = NSSize(width: 0, height: -10)
        shadow.set()
        NSColor(srgbRed: 0.16, green: 0.42, blue: 0.84, alpha: 1).setFill()
        tile.fill()
        NSGraphicsContext.restoreGraphicsState()
        NSGradient(
          starting: NSColor(srgbRed: 0.22, green: 0.49, blue: 0.94, alpha: 1),
          ending: NSColor(srgbRed: 0.12, green: 0.33, blue: 0.74, alpha: 1)
        )!.draw(in: tile, angle: -90)
        mark.draw(in: NSRect(x: 240, y: 240, width: 544, height: 544))
        NSGraphicsContext.restoreGraphicsState()
        let suffix = scale == 2 ? "@2x" : ""
        let file = output.appendingPathComponent("icon_\(size)x\(size)\(suffix).png")
        try bitmap.representation(using: .png, properties: [:])!.write(to: file)
      }
    }
  }
}
