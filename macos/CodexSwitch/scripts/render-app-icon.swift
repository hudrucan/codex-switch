import AppKit
import Foundation

// Compile with Sources/CodexSwitchApp/MenuBarIcon.swift to share vector geometry.
@main @MainActor
struct RenderAppIcon {
  static func main() throws {
    guard CommandLine.arguments.count == 3 else {
      fatalError("Usage: render-app-icon output.iconset asset-directory")
    }
    let output = URL(fileURLWithPath: CommandLine.arguments[1])
    let assets = URL(fileURLWithPath: CommandLine.arguments[2])
    for directory in [output, assets] {
      try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }
    for size in [16, 32, 128, 256, 512] {
      for scale in [1, 2] {
        let suffix = scale == 2 ? "@2x" : ""
        try render(pixels: size * scale, canvas: 1024, draw: drawAppIcon)
          .write(to: output.appendingPathComponent("icon_\(size)x\(size)\(suffix).png"))
      }
    }
    try render(pixels: 24, canvas: 24) { context in
      LayersMark.drawOutline(in: context, color: NSColor.white.cgColor)
    }.write(to: assets.appendingPathComponent("sidebar-outline-24.png"))
    try sidebarOutlineSVG.write(
      to: assets.appendingPathComponent("sidebar-outline.svg"), atomically: true, encoding: .utf8)
  }

  static func render(pixels: Int, canvas: CGFloat, draw: (CGContext) -> Void) throws -> Data {
    let bitmap = NSBitmapImageRep(
      bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
      bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
      colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    defer { NSGraphicsContext.restoreGraphicsState() }
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
    let context = NSGraphicsContext.current!.cgContext
    context.translateBy(x: 0, y: CGFloat(pixels))
    context.scaleBy(x: CGFloat(pixels) / canvas, y: -CGFloat(pixels) / canvas)
    draw(context)
    context.flush()
    guard let png = bitmap.representation(using: .png, properties: [:]) else {
      throw CocoaError(.fileWriteUnknown)
    }
    return png
  }

  static func color(_ hex: UInt32) -> NSColor {
    NSColor(
      srgbRed: CGFloat((hex >> 16) & 255) / 255,
      green: CGFloat((hex >> 8) & 255) / 255,
      blue: CGFloat(hex & 255) / 255, alpha: 1)
  }

  static func gradient(_ path: CGPath, from start: UInt32, to end: UInt32) {
    let context = NSGraphicsContext.current!.cgContext
    context.saveGState()
    context.addPath(path)
    context.clip()
    let bounds = path.boundingBoxOfPath
    let gradient = CGGradient(
      colorsSpace: CGColorSpace(name: CGColorSpace.sRGB),
      colors: [color(start).cgColor, color(end).cgColor] as CFArray,
      locations: [0, 1])!
    context.drawLinearGradient(
      gradient, start: CGPoint(x: bounds.minX, y: bounds.minY),
      end: CGPoint(x: bounds.maxX, y: bounds.maxY), options: [])
    context.restoreGState()
  }

  static func drawAppIcon(_ context: CGContext) {
    let tile = CGPath(
      roundedRect: CGRect(x: 80, y: 80, width: 864, height: 864),
      cornerWidth: 202, cornerHeight: 202, transform: nil)
    gradient(tile, from: 0x29385B, to: 0x090F24)
    context.setStrokeColor(color(0x6574AD).withAlphaComponent(0.65).cgColor)
    LayersMark.stroke(tile, in: context, width: 3)

    context.saveGState()
    context.translateBy(x: 220, y: 205)
    context.scaleBy(x: 25, y: 25)
    context.saveGState()
    context.setShadow(
      offset: CGSize(width: 0, height: -0.5), blur: 1.2,
      color: NSColor.black.withAlphaComponent(0.35).cgColor)
    context.setFillColor(color(0x5464FF).cgColor)
    context.addPath(LayersMark.rear)
    context.fillPath()
    context.restoreGState()
    gradient(LayersMark.rear, from: 0x91BFFF, to: 0x6542F5)
    context.setStrokeColor(color(0xC4CCFF).withAlphaComponent(0.85).cgColor)
    LayersMark.stroke(LayersMark.rear, in: context, width: 0.16)

    context.saveGState()
    context.setShadow(
      offset: CGSize(width: 0, height: -0.6), blur: 1,
      color: NSColor.black.withAlphaComponent(0.5).cgColor)
    context.setFillColor(color(0x10152F).cgColor)
    context.addPath(LayersMark.front)
    context.fillPath()
    context.restoreGState()
    gradient(LayersMark.front, from: 0x293454, to: 0x141137)
    context.setStrokeColor(color(0x8884D5).withAlphaComponent(0.8).cgColor)
    LayersMark.stroke(LayersMark.front, in: context, width: 0.2)
    context.setStrokeColor(color(0xFCFCFF).cgColor)
    LayersMark.stroke(LayersMark.chevron, in: context, width: 1.9)
    context.setStrokeColor(color(0x9A8AFF).cgColor)
    LayersMark.stroke(LayersMark.underscore, in: context, width: 1.9)
    context.restoreGState()
  }

  // Serialize the actual master paths so the export cannot drift from the app.
  static func svgPath(_ path: CGPath) -> String {
    var commands: [String] = []
    path.applyWithBlock { element in
      let points = element.pointee.points
      func point(_ index: Int) -> String { "\(points[index].x) \(points[index].y)" }
      switch element.pointee.type {
      case .moveToPoint: commands.append("M\(point(0))")
      case .addLineToPoint: commands.append("L\(point(0))")
      case .addQuadCurveToPoint: commands.append("Q\(point(0)) \(point(1))")
      case .addCurveToPoint: commands.append("C\(point(0)) \(point(1)) \(point(2))")
      case .closeSubpath: commands.append("Z")
      @unknown default: preconditionFailure("Unsupported vector command")
      }
    }
    return commands.joined(separator: " ")
  }

  static var sidebarOutlineSVG: String {
    """
    <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">
      <title>Codex Switch — Layers Outline</title>
      <defs>
        <mask id="layers" maskUnits="userSpaceOnUse" x="0" y="0" width="24" height="24" style="mask-type:luminance">
          <g fill="none" stroke-linecap="round" stroke-linejoin="round">
            <path d="\(svgPath(LayersMark.rear))" stroke="#FFFFFF" stroke-width="1.5"/>
            <path d="\(svgPath(LayersMark.front))" fill="#000000" stroke="#000000" stroke-width="3.8"/>
            <path d="\(svgPath(LayersMark.front))" stroke="#FFFFFF" stroke-width="1.5"/>
            <path d="\(svgPath(LayersMark.chevron)) \(svgPath(LayersMark.underscore))" stroke="#FFFFFF" stroke-width="1.5"/>
          </g>
        </mask>
      </defs>
      <path fill="#FFFFFF" mask="url(#layers)" d="M0 0H24V24H0Z"/>
    </svg>
    """
  }
}
