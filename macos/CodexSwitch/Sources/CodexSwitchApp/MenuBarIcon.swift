import AppKit

@MainActor
enum MenuBarIcon {
  static let image: NSImage = {
    let image = NSImage(size: NSSize(width: 18, height: 18), flipped: true) { _ in
      NSGraphicsContext.saveGraphicsState()
      let inset = NSAffineTransform()
      inset.translateX(by: 0.95, yBy: 1.3)
      inset.scale(by: 0.85)
      inset.concat()
      NSColor.black.setStroke()
      let head = NSBezierPath(ovalIn: NSRect(x: 4, y: 1, width: 7.4, height: 7.4))
      head.lineWidth = 1.7
      head.stroke()
      let shoulder = NSBezierPath()
      shoulder.lineWidth = 1.7
      shoulder.lineCapStyle = .round
      shoulder.move(to: NSPoint(x: 1.3, y: 16.5))
      shoulder.curve(
        to: NSPoint(x: 7.7, y: 8.6),
        controlPoint1: NSPoint(x: 1.3, y: 12), controlPoint2: NSPoint(x: 4, y: 8.6))
      shoulder.stroke()
      NSGraphicsContext.restoreGraphicsState()

      NSColor.black.setStroke()
      let strokes = NSBezierPath()
      strokes.lineWidth = 1.7
      strokes.lineCapStyle = .round
      strokes.lineJoinStyle = .round
      strokes.move(to: NSPoint(x: 17, y: 11.4))
      strokes.line(to: NSPoint(x: 10.8, y: 11.4))
      strokes.line(to: NSPoint(x: 12.5, y: 9.7))
      strokes.move(to: NSPoint(x: 10.8, y: 14.6))
      strokes.line(to: NSPoint(x: 17, y: 14.6))
      strokes.line(to: NSPoint(x: 15.3, y: 16.3))
      strokes.stroke()
      return true
    }
    image.isTemplate = true
    image.accessibilityDescription = "Codex Switch"
    return image
  }()
}
