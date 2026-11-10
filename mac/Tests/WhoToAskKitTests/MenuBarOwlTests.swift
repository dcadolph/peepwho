import AppKit
import Testing

@testable import WhoToAskKit

// alphaOfOwl draws the mark at a display's scale and reads its coverage at a point on the path.
func alphaOfOwl(x: CGFloat, y: CGFloat, scale: Int = 2) throws -> CGFloat {
    let size = MenuBarOwl.size
    let rep = try #require(
        NSBitmapImageRep(
            bitmapDataPlanes: nil, pixelsWide: Int(size.width) * scale, pixelsHigh: Int(size.height) * scale,
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
        )
    )
    rep.size = size
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    MenuBarOwl.image.draw(in: NSRect(origin: .zero, size: size))
    NSGraphicsContext.restoreGraphicsState()
    let point = MenuBarOwl.point(x: x, y: y)
    let color = try #require(rep.colorAt(x: Int(point.x) * scale, y: Int(point.y) * scale))
    return color.alphaComponent
}

// MenuBarOwlTests holds the mark to a tintable owl, solid in the body and open at the eyes.
@Suite struct MenuBarOwlTests {
    // templateLetsTheSystemTint holds the flag that makes it white on a dark menu bar.
    @Test func templateLetsTheSystemTint() {
        #expect(MenuBarOwl.image.isTemplate)
        #expect(MenuBarOwl.image.accessibilityDescription == "Socrates, the WhoToAsk owl")
    }

    // fitsTheMenuBar holds the size to what a menu bar holds without cropping.
    @Test func fitsTheMenuBar() {
        #expect(MenuBarOwl.image.size == NSSize(width: 14, height: 18))
    }

    // bodyIsSolid reads fully opaque on the chest and head at both display scales.
    @Test(arguments: [1, 2]) func bodyIsSolid(scale: Int) throws {
        #expect(try alphaOfOwl(x: 511.6, y: 750, scale: scale) == 1)
        #expect(try alphaOfOwl(x: 511.6, y: 340, scale: scale) == 1)
    }

    // eyesAndBeakAreOpen reads clear through both eyes and the beak on a Retina display.
    @Test func eyesAndBeakAreOpen() throws {
        #expect(try alphaOfOwl(x: 390, y: 430) == 0)
        #expect(try alphaOfOwl(x: 635, y: 450) == 0)
        #expect(try alphaOfOwl(x: 511.6, y: 515) < 0.5)
    }

    // nothingOutsideTheOwl reads clear between the ear tufts and beside the base.
    @Test func nothingOutsideTheOwl() throws {
        #expect(try alphaOfOwl(x: 511.6, y: 160) == 0)
        #expect(try alphaOfOwl(x: 240, y: 800) == 0)
    }

    // artFillsTheHeight puts the tuft tips at the top and the base at the bottom of the mark.
    @Test func artFillsTheHeight() {
        #expect(MenuBarOwl.point(x: 511.6, y: 121.7).y == 0)
        #expect(MenuBarOwl.point(x: 511.6, y: 918.7).y == MenuBarOwl.size.height)
        #expect(MenuBarOwl.point(x: 511.6, y: 500).x == MenuBarOwl.size.width / 2)
    }
}
