import AppKit
import Testing

@testable import WhoToAskKit

// WindowFitTests pin that the menu bar window shrinks and grows from its top edge.
struct WindowFitTests {
    // Test 0: Resizing keeps the top edge, the left edge, and the width.
    @Test(arguments: [
        (NSRect(x: 100, y: 400, width: 360, height: 440), CGFloat(190),
         NSRect(x: 100, y: 650, width: 360, height: 190) as NSRect?),
        (NSRect(x: 100, y: 650, width: 360, height: 190), CGFloat(440),
         NSRect(x: 100, y: 400, width: 360, height: 440) as NSRect?),
        (NSRect(x: 100, y: 650, width: 360, height: 190), CGFloat(190), nil),
        (NSRect(x: 100, y: 650, width: 360, height: 190), CGFloat(190.2), nil),
        (NSRect(x: 100, y: 650, width: 360, height: 190), CGFloat(0), nil),
    ])
    func frame(current: NSRect, height: CGFloat, want: NSRect?) {
        #expect(WindowFit.frame(current, height: height) == want)
    }
}
