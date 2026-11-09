import AppKit

// MenuBarOwl is the app's owl as a menu bar mark: the site's white owl with its eyes and beak cut
// clean through. It is a template image, so macOS paints it white on a dark menu bar and dark on a
// light one, and it draws from vectors so it stays sharp on every display.
public enum MenuBarOwl {
    // size is the mark's size in points, the height of a menu bar symbol at the owl's proportions.
    public static let size = NSSize(width: 14, height: 18)
    // art is the owl's extent in the coordinates of its path.
    static let art = NSRect(x: 216.4, y: 121.7, width: 590.4, height: 797)
    // outline is the body with its eyes and beak, in site/favicon.svg's coordinates. The site's two
    // elliptical arcs are written here as the cubic curves they equal.
    private static let outline = """
        M 511.6 918.7 L 343 918.7 C 320.1 918.7 298.1 898.2 305.4 862.3 L 396.3 611.7 \
        C 325.7 584.2 273 533 251.8 471.1 C 230.7 409.3 243 342.9 285.8 289.1 \
        C 244.3 240.5 219.7 187 216.4 121.7 C 259.7 156.1 327.2 212.7 383.8 217.2 \
        C 446.2 222.2 476.7 243.9 511.6 295.6 C 546.5 243.9 577 222.2 639.4 217.2 \
        C 696 212.7 763.5 156.1 806.8 121.7 C 803.5 187 778.9 240.5 737.3 289.1 \
        C 780.1 342.9 792.5 409.2 771.3 471.1 C 750.1 532.9 697.5 584.2 626.9 611.7 \
        L 717.8 862.3 C 725.1 898.2 703.1 918.7 680.2 918.7 L 511.6 918.7 Z \
        M 327.5 326.6 L 487.5 425.4 C 472.7 458.3 435.6 501.4 395.2 501.4 \
        C 350.5 501.4 304.6 451 304.6 406.7 C 304.6 369.6 311.7 358.1 327.5 326.6 Z \
        M 548.7 445.9 L 720.2 379.7 C 720.2 433.7 695.2 500.6 629.6 500.6 \
        C 592.3 500.6 569.1 473 548.7 445.9 Z \
        M 461.8 493.2 C 495 470.9 528.2 470.9 561.4 493.2 L 511.6 580.6 L 461.8 493.2 Z
        """

    // image is the template image the menu bar shows.
    public static let image: NSImage = {
        let image = NSImage(size: size, flipped: true) { _ in
            NSColor.black.setFill()
            path().fill()
            return true
        }
        image.isTemplate = true
        image.accessibilityDescription = "peepwho"
        return image
    }()

    // point places a point from the path's coordinates in the mark, the owl centered at full height.
    static func point(x: CGFloat, y: CGFloat) -> NSPoint {
        let scale = size.height / art.height
        return NSPoint(
            x: (x - art.midX) * scale + size.width / 2,
            y: (y - art.minY) * scale
        )
    }

    // path turns the outline into a path whose inner shapes stay holes.
    private static func path() -> NSBezierPath {
        let path = NSBezierPath()
        path.windingRule = .evenOdd
        let tokens = outline.split(separator: " ")
        var next = 0
        func take() -> NSPoint {
            defer { next += 2 }
            return point(x: CGFloat(Double(tokens[next])!), y: CGFloat(Double(tokens[next + 1])!))
        }
        while next < tokens.count {
            let command = tokens[next]
            next += 1
            switch command {
            case "M": path.move(to: take())
            case "L": path.line(to: take())
            case "C":
                let first = take()
                let second = take()
                path.curve(to: take(), controlPoint1: first, controlPoint2: second)
            default: path.close()
            }
        }
        return path
    }
}
