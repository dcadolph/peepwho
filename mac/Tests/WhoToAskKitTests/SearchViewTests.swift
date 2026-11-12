import AppKit
import SwiftUI
import Testing

@testable import WhoToAsk
@testable import WhoToAskKit

// SearchWindow is the real search window hosted over the demo index.
@MainActor
struct SearchWindow {
    // hosted is the search window in its fence.
    let hosted: Hosted<SearchView>
    // model answers the field.
    let model: SearchModel
    // core is the open demo index.
    let core: Core
    // opened records the links the window opens.
    let opened: Opened
    // dir is the demo index directory, removed by close.
    let dir: URL

    // open hosts the search window over a fresh demo index, answering after delay.
    static func open(delay: Duration = .milliseconds(1)) throws -> SearchWindow {
        let dir = try demoDir()
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: CoreMemorySecrets(MemorySecrets()))
        let model = SearchModel(core: core, delay: delay, opener: opened.open)
        let hosted = host(SearchView(model: model), size: NSSize(width: 360, height: 500))
        return SearchWindow(hosted: hosted, model: model, core: core, opened: opened, dir: dir)
    }

    // ask types text into the field and waits until the window shows the answer to all of it
    // with count people, so an answer to a shorter question still on its way cannot land later.
    func ask(_ text: String, people count: Int) async -> Bool {
        hosted.type(text)
        return await waitFor { model.answered == text && model.people.count == count }
    }

    // marked returns the names whose rows carry the mark in the rendered window, read in the
    // middle of each name's line, where only the mark's fill is drawn.
    func marked(_ names: [String]) throws -> [String] {
        let rep = try hosted.render()
        let lines = try recognize(rep)
        let scale = CGFloat(rep.pixelsWide) / rep.size.width
        return names.filter { name in
            guard let line = lines.first(where: { $0.text.contains(name) }) else { return false }
            let pixel = rep.colorAt(x: Int(200 * scale), y: Int(line.box.midY * scale))
            return (pixel?.alphaComponent ?? 0) > 0.03
        }
    }

    // bands returns the top pixel row of each run of rows that draw something opaque in the
    // window's right-hand 80 points, where only the strength bars and the buttons sit, so a row
    // that moves moves its band. The mark's fill is too faint to count.
    func bands() throws -> [Int] {
        let rep = try hosted.render()
        let scale = CGFloat(rep.pixelsWide) / rep.size.width
        let left = Int((rep.size.width - 80) * scale)
        var tops: [Int] = []
        var inside = false
        for y in 0..<rep.pixelsHigh {
            let opaque = (left..<rep.pixelsWide).contains { x in
                (rep.colorAt(x: x, y: y)?.alphaComponent ?? 0) > 0.5
            }
            if opaque, !inside { tops.append(y) }
            inside = opaque
        }
        return tops
    }

    // close takes the window down and removes the demo index.
    func close() {
        hosted.window.close()
        try? FileManager.default.removeItem(at: dir)
    }
}

extension ViewTests {
    // SearchViewTests drive the real search window with key presses and read what it shows.
    @MainActor
    @Suite
    struct SearchViewTests {
        // Test 0: Down twice marks the third person, the window and its content keep their size
        // while the mark moves, so no row jumps, and Enter opens the third person's link.
        @Test func downTwiceThenEnter() async throws {
            let search = try SearchWindow.open()
            defer { search.close() }
            #expect(await search.ask("billing kafka", people: 4))
            let names = search.model.people.map(\.name)
            _ = try await search.hosted.read(until: names[3])
            await search.hosted.steady()
            #expect(try search.marked(names) == [names[0]])
            let frame = search.hosted.window.frame
            let fitting = search.hosted.view.fittingSize
            let bands = try search.bands()
            #expect(bands.count >= 4, "bands \(bands)")
            search.hosted.press(.down)
            search.hosted.press(.down)
            #expect(search.model.selected == 2)
            let hint = "Enter messages \(names[2])."
            let lines = try await search.hosted.read(until: hint).map(\.text)
            #expect(lines.contains { $0.contains(hint) }, "read \(lines)")
            await search.hosted.steady()
            #expect(try search.marked(names) == [names[2]])
            #expect(search.hosted.window.frame == frame)
            #expect(search.hosted.view.fittingSize == fitting)
            #expect(try search.bands() == bands)
            search.hosted.press(.enter)
            #expect(await waitFor(3) { !search.opened.urls.isEmpty })
            let third = try await search.core.person(search.model.people[2].id)
            let want = try #require(third.links?.first)
            #expect(search.opened.urls.map(\.absoluteString) == [want])
            search.hosted.expectUntouched()
        }

        // Test 1: Escape on a person's page goes back to the list with the same person marked, and
        // Up stops at the first row.
        @Test func escapeKeepsTheMark() async throws {
            let search = try SearchWindow.open()
            defer { search.close() }
            #expect(await search.ask("billing kafka", people: 4))
            let names = search.model.people.map(\.name)
            search.hosted.press(.down)
            search.hosted.press(.down)
            search.hosted.press(.commandEnter)
            #expect(await waitFor(3) { search.model.detail?.name == names[2] })
            search.hosted.press(.escape)
            #expect(await waitFor(3) { search.model.detail == nil })
            #expect(search.model.selected == 2)
            _ = try await search.hosted.read(until: "Enter messages \(names[2]).")
            #expect(try search.marked(names) == [names[2]])
            for _ in 0..<4 { search.hosted.press(.up) }
            #expect(search.model.selected == 0)
            _ = try await search.hosted.read(until: "Enter messages the first person.")
            #expect(try search.marked(names) == [names[0]])
            #expect(search.opened.urls.isEmpty)
            search.hosted.expectUntouched()
        }

        // Test 2: Command-Return pressed right after typing a new question shows the first person
        // of the new answer, not the person marked in the old one.
        @Test func commandReturnRightAfterTyping() async throws {
            let search = try SearchWindow.open(delay: .milliseconds(200))
            defer { search.close() }
            #expect(await search.ask("billing", people: 3))
            search.hosted.press(.down)
            let old = search.model.marked?.name
            for _ in 0..<"billing".count { search.hosted.press(.delete) }
            search.hosted.type("terraform")
            search.hosted.press(.commandEnter)
            #expect(await waitFor(3) { search.model.detail != nil })
            #expect(search.model.detail?.name == "Carol Lee")
            #expect(old != "Carol Lee")
            settle(0.5)
            #expect(search.model.detail?.name == "Carol Lee")
            let lines = try await search.hosted.read(until: "Carol Lee").map(\.text)
            #expect(lines.contains { $0.contains("Carol Lee") }, "read \(lines)")
            search.hosted.expectUntouched()
        }
    }
}
