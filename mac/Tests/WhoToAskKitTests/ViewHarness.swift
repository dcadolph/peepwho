import AppKit
import SwiftUI
import Testing
import Vision

@testable import WhoToAsk
@testable import WhoToAskKit

// FencedWindow is a window no view can order in or make key, so a hosted view never reaches the
// screen or takes focus from the person at the Mac. It reports itself key, so the hosted view
// behaves as it does in the focused window.
final class FencedWindow: NSWindow {
    // fenced lists every attempt to show the window or make it key, in order.
    var fenced: [String] = []

    // makeKeyAndOrderFront records the attempt and leaves the window out.
    override func makeKeyAndOrderFront(_ sender: Any?) { fenced.append("makeKeyAndOrderFront") }

    // orderFront records the attempt and leaves the window out.
    override func orderFront(_ sender: Any?) { fenced.append("orderFront") }

    // orderFrontRegardless records the attempt and leaves the window out.
    override func orderFrontRegardless() { fenced.append("orderFrontRegardless") }

    // order records any attempt to order the window in and leaves it out.
    override func order(_ place: NSWindow.OrderingMode, relativeTo otherWin: Int) {
        if place != .out { fenced.append("order") }
    }

    // makeKey records the attempt and leaves key status alone.
    override func makeKey() { fenced.append("makeKey") }

    // makeMain records the attempt and leaves main status alone.
    override func makeMain() { fenced.append("makeMain") }

    // isKeyWindow is always true, as for the window the person is typing in.
    override var isKeyWindow: Bool { true }

    // canBecomeKey is true so the field can hold the cursor.
    override var canBecomeKey: Bool { true }
}

// TerminationGuard refuses every request to quit, since a hosted view's Quit button would end
// the test run with no results.
final class TerminationGuard: NSObject, NSApplicationDelegate {
    // asked counts the refused requests.
    var asked = 0

    // applicationShouldTerminate counts the request and refuses it.
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        asked += 1
        return .terminateCancel
    }
}

// terminationGuard is held here because NSApplication keeps its delegate weakly.
@MainActor let terminationGuard = TerminationGuard()

// Key is one key press as AppKit delivers it.
struct Key {
    // characters is the text the key types.
    let characters: String
    // code is the hardware key code.
    let code: UInt16
    // modifiers are the modifier keys held.
    let modifiers: NSEvent.ModifierFlags

    // down is the Down arrow.
    static let down = Key(characters: "\u{F701}", code: 125, modifiers: [.numericPad, .function])
    // up is the Up arrow.
    static let up = Key(characters: "\u{F700}", code: 126, modifiers: [.numericPad, .function])
    // escape is the Escape key.
    static let escape = Key(characters: "\u{1B}", code: 53, modifiers: [])
    // enter is the Return key.
    static let enter = Key(characters: "\r", code: 36, modifiers: [])
    // commandEnter is Command-Return.
    static let commandEnter = Key(characters: "\r", code: 36, modifiers: .command)
    // delete is the Delete key.
    static let delete = Key(characters: "\u{7F}", code: 51, modifiers: [])
}

// TextLine is one line of text read from a rendered view.
struct TextLine {
    // text is what was read.
    let text: String
    // box is where it sits, in view points from the top left.
    let box: CGRect
}

// Hosted is a view in a fenced window, with what a test needs to drive and read it.
@MainActor
struct Hosted<V: View> {
    // window is the fenced window the view sits in.
    let window: FencedWindow
    // view is the hosting view.
    let view: NSHostingView<V>

    // press delivers key down and up the way NSApplication does for its key window: a Command
    // key is offered as a key equivalent first, then everything goes through the window.
    func press(_ key: Key) {
        for type in [NSEvent.EventType.keyDown, .keyUp] {
            guard let event = NSEvent.keyEvent(
                with: type, location: .zero, modifierFlags: key.modifiers,
                timestamp: ProcessInfo.processInfo.systemUptime,
                windowNumber: window.windowNumber, context: nil, characters: key.characters,
                charactersIgnoringModifiers: key.characters, isARepeat: false, keyCode: key.code
            ) else { continue }
            let command = type == .keyDown && key.modifiers.contains(.command)
            if command, window.performKeyEquivalent(with: event) { continue }
            window.sendEvent(event)
        }
        settle(0.03)
    }

    // type presses one key per character of text.
    func type(_ text: String) {
        for character in text {
            let code: UInt16 = character == " " ? 49 : 0
            press(Key(characters: String(character), code: code, modifiers: []))
        }
    }

    // render draws the view at twice its point size.
    func render() throws -> NSBitmapImageRep {
        let size = view.bounds.size
        let rep = try #require(NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: Int(size.width * 2), pixelsHigh: Int(size.height * 2),
            bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
            colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
        ))
        rep.size = size
        view.cacheDisplay(in: view.bounds, to: rep)
        return rep
    }

    // read renders the view and returns each line of text recognized in it.
    func read() throws -> [TextLine] {
        try recognize(render())
    }

    // read renders and reads the view until a line contains want or timeout passes, since the
    // window's layout can trail the model under load, and returns the last lines read.
    func read(until want: String, timeout: TimeInterval = 5) async throws -> [TextLine] {
        let end = Date().addingTimeInterval(timeout)
        var lines = try read()
        while !lines.contains(where: { $0.text.contains(want) }), Date() < end {
            settle(0.1)
            await Task.yield()
            lines = try read()
        }
        return lines
    }

    // expectUntouched checks that nothing reached the screen or took focus: the fence stopped
    // nothing, the window is out, and this process is neither active nor frontmost.
    func expectUntouched(sourceLocation: SourceLocation = #_sourceLocation) {
        #expect(window.fenced.isEmpty, "fenced \(window.fenced)", sourceLocation: sourceLocation)
        #expect(!window.isVisible, sourceLocation: sourceLocation)
        #expect(!NSApp.isActive, sourceLocation: sourceLocation)
        let front = NSWorkspace.shared.frontmostApplication?.processIdentifier
        #expect(front != ProcessInfo.processInfo.processIdentifier, sourceLocation: sourceLocation)
    }
}

// host puts view in a fenced window of size, under the light appearance, with this process kept
// from ever becoming active and from quitting.
@MainActor
func host<V: View>(_ view: V, size: NSSize) -> Hosted<V> {
    _ = NSApplication.shared.setActivationPolicy(.prohibited)
    NSApp.delegate = terminationGuard
    let window = FencedWindow(
        contentRect: NSRect(x: -30000, y: -30000, width: size.width, height: size.height),
        styleMask: [.titled, .closable], backing: .buffered, defer: false
    )
    window.isReleasedWhenClosed = false
    window.appearance = NSAppearance(named: .aqua)
    let hosting = NSHostingView(rootView: view)
    hosting.frame = NSRect(origin: .zero, size: size)
    window.contentView = hosting
    settle(0.2)
    return Hosted(window: window, view: hosting)
}

// settle runs the main run loop for seconds, so views lay out and events land.
@MainActor
func settle(_ seconds: TimeInterval) {
    let end = Date().addingTimeInterval(seconds)
    while Date() < end {
        RunLoop.main.run(until: Date().addingTimeInterval(0.01))
    }
}

// waitFor runs the main run loop until done reports true or timeout passes, and returns done's
// last answer.
@MainActor
func waitFor(_ timeout: TimeInterval = 15, _ done: () -> Bool) async -> Bool {
    let end = Date().addingTimeInterval(timeout)
    while Date() < end {
        if done() { return true }
        settle(0.02)
        await Task.yield()
    }
    return done()
}

// recognize reads the text in rep, drawn over white first, since dark text over a clear
// backdrop reads poorly.
func recognize(_ rep: NSBitmapImageRep) throws -> [TextLine] {
    let clear = try #require(rep.cgImage)
    let context = try #require(CGContext(
        data: nil, width: clear.width, height: clear.height, bitsPerComponent: 8, bytesPerRow: 0,
        space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue
    ))
    let whole = CGRect(x: 0, y: 0, width: clear.width, height: clear.height)
    context.setFillColor(CGColor(gray: 1, alpha: 1))
    context.fill(whole)
    context.draw(clear, in: whole)
    let image = try #require(context.makeImage())
    let request = VNRecognizeTextRequest()
    request.recognitionLevel = .accurate
    request.usesLanguageCorrection = false
    try VNImageRequestHandler(cgImage: image).perform([request])
    let width = rep.size.width
    let height = rep.size.height
    return (request.results ?? []).compactMap { observation in
        guard let best = observation.topCandidates(1).first else { return nil }
        let box = observation.boundingBox
        return TextLine(text: best.string, box: CGRect(
            x: box.minX * width, y: (1 - box.maxY) * height,
            width: box.width * width, height: box.height * height
        ))
    }
}

// ViewHarnessTests pin that the harness drives a real view and never touches the screen.
@MainActor
@Suite(.serialized)
struct ViewHarnessTests {
    // Test 0: Keys typed into the real search window answer, the answer reads back from the
    // rendered window, and nothing reaches the screen.
    @Test func searchAnswersTypedKeys() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let core = try Core.open(dir: dir, key: "", secrets: CoreMemorySecrets(MemorySecrets()))
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: Opened().open)
        let hosted = host(SearchView(model: model), size: NSSize(width: 360, height: 500))
        defer { hosted.window.close() }
        hosted.type("terraform")
        #expect(await waitFor { model.people.first?.name == "Carol Lee" })
        let lines = try await hosted.read(until: "Carol Lee").map(\.text)
        #expect(lines.contains { $0.contains("Carol Lee") }, "read \(lines)")
        hosted.expectUntouched()
    }

    // Test 1: The fence stops a view that tries to show its window or make it key.
    @Test func fenceStopsShowing() {
        let hosted = host(Text("Fenced"), size: NSSize(width: 100, height: 40))
        defer { hosted.window.close() }
        hosted.window.makeKeyAndOrderFront(nil)
        hosted.window.orderFrontRegardless()
        #expect(hosted.window.fenced == ["makeKeyAndOrderFront", "orderFrontRegardless"])
        #expect(!hosted.window.isVisible)
    }

    // Test 2: The Quit button's request to quit is refused while views are hosted.
    @Test func quitIsRefused() {
        let hosted = host(Text("Guarded"), size: NSSize(width: 100, height: 40))
        defer { hosted.window.close() }
        let before = terminationGuard.asked
        NSApp.terminate(nil)
        #expect(terminationGuard.asked == before + 1)
    }
}
