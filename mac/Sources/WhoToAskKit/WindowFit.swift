import AppKit

// WindowFit sizes the menu bar window to its content, since the window grows with its content
// but never shrinks on its own and centers short content in the space left over.
public enum WindowFit {
    // frame returns current resized to height with its top edge where it was, so the window stays
    // under the menu bar. It returns nil when height is not positive or the size already matches.
    public static func frame(_ current: NSRect, height: CGFloat) -> NSRect? {
        guard height > 0, abs(current.height - height) >= 0.5 else { return nil }
        return NSRect(x: current.minX, y: current.maxY - height, width: current.width, height: height)
    }

    // apply resizes window so its content area is height tall, keeping its top edge in place.
    @MainActor
    public static func apply(_ window: NSWindow?, height: CGFloat) {
        guard let window else { return }
        let content = window.contentRect(forFrameRect: window.frame)
        guard let fitted = frame(content, height: height) else { return }
        window.setFrame(window.frameRect(forContentRect: fitted), display: true)
    }
}
