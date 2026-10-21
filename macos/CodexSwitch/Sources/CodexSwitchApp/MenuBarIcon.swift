import AppKit

// All variants use this 24-point vector master, including the standalone SVG.
@MainActor
enum LayersMark {
  static let rear: CGPath = {
    let path = CGMutablePath()
    path.move(to: CGPoint(x: 2, y: 16))
    path.addLine(to: CGPoint(x: 2, y: 6))
    path.addCurve(
      to: CGPoint(x: 6, y: 2.5), control1: CGPoint(x: 2, y: 4), control2: CGPoint(x: 3.5, y: 2.8))
    path.addLine(to: CGPoint(x: 13, y: 1.5))
    path.addCurve(
      to: CGPoint(x: 17, y: 5), control1: CGPoint(x: 15.5, y: 1.1), control2: CGPoint(x: 17, y: 2.5)
    )
    path.addLine(to: CGPoint(x: 17, y: 16))
    path.addQuadCurve(to: CGPoint(x: 14, y: 19), control: CGPoint(x: 17, y: 19))
    path.addLine(to: CGPoint(x: 5, y: 19))
    path.addQuadCurve(to: CGPoint(x: 2, y: 16), control: CGPoint(x: 2, y: 19))
    path.closeSubpath()
    return path
  }()

  static let front: CGPath = {
    let path = CGMutablePath()
    path.move(to: CGPoint(x: 6, y: 9))
    path.addCurve(
      to: CGPoint(x: 10, y: 5.5), control1: CGPoint(x: 6, y: 6.5), control2: CGPoint(x: 7.5, y: 5))
    path.addLine(to: CGPoint(x: 19, y: 7))
    path.addQuadCurve(to: CGPoint(x: 22.5, y: 11), control: CGPoint(x: 22.5, y: 7.5))
    path.addLine(to: CGPoint(x: 22.5, y: 19))
    path.addCurve(
      to: CGPoint(x: 18.5, y: 22.5), control1: CGPoint(x: 22.5, y: 21.5),
      control2: CGPoint(x: 21, y: 23))
    path.addLine(to: CGPoint(x: 9.5, y: 21))
    path.addQuadCurve(to: CGPoint(x: 6, y: 17), control: CGPoint(x: 6, y: 20.5))
    path.closeSubpath()
    return path
  }()

  static let chevron: CGPath = {
    let path = CGMutablePath()
    path.move(to: CGPoint(x: 10, y: 10.5))
    path.addLine(to: CGPoint(x: 12.8, y: 13.5))
    path.addLine(to: CGPoint(x: 10, y: 16.5))
    return path
  }()

  static let underscore: CGPath = {
    let path = CGMutablePath()
    path.move(to: CGPoint(x: 15.5, y: 17))
    path.addLine(to: CGPoint(x: 19, y: 17.5))
    return path
  }()

  static func stroke(_ path: CGPath, in context: CGContext, width: CGFloat) {
    context.addPath(path)
    context.setLineWidth(width)
    context.setLineCap(.round)
    context.setLineJoin(.round)
    context.strokePath()
  }

  static func drawOutline(in context: CGContext, color: CGColor, lineWidth: CGFloat = 1.5) {
    context.saveGState()
    context.setStrokeColor(color)
    stroke(rear, in: context, width: lineWidth)
    context.setBlendMode(.clear)
    context.addPath(front)
    context.fillPath()
    stroke(front, in: context, width: lineWidth + 2.3)
    context.setBlendMode(.normal)
    stroke(front, in: context, width: lineWidth)
    stroke(chevron, in: context, width: lineWidth)
    stroke(underscore, in: context, width: lineWidth)
    context.restoreGState()
  }

}

@MainActor
enum MenuBarIcon {
  static let image: NSImage = {
    let image = NSImage(size: NSSize(width: 18, height: 18), flipped: true) { _ in
      guard let context = NSGraphicsContext.current?.cgContext else { return false }
      context.saveGState()
      context.scaleBy(x: 18 / 24, y: 18 / 24)
      // Keep the master silhouette and full >_ glyph with a 1.5pt menu-bar stroke.
      LayersMark.drawOutline(in: context, color: NSColor.black.cgColor, lineWidth: 2)
      context.restoreGState()
      return true
    }
    image.isTemplate = true
    image.accessibilityDescription = "Codex Switch"
    return image
  }()
}
