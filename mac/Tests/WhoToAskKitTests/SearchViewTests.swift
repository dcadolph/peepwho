import AppKit
import SwiftUI
import Testing

@testable import WhoToAsk
@testable import WhoToAskKit

// Spoken records what the search window hands VoiceOver to read.
@MainActor
final class Spoken {
    // names are the names handed over, in order.
    var names: [String] = []
}

// SearchWindow is the real search window hosted over the demo index.
@MainActor
struct SearchWindow {
    // hosted is the search window in its fence.
    let hosted: Hosted<AnyView>
    // model answers the field.
    let model: SearchModel
    // core is the open demo index.
    let core: Core
    // opened records the links the window opens.
    let opened: Opened
    // spoken records the names handed to VoiceOver.
    let spoken: Spoken
    // dir is the demo index directory, removed by close.
    let dir: URL

    // open hosts the search window over a fresh demo index, answering after delay, under the
    // system contrast setting contrast.
    static func open(
        delay: Duration = .milliseconds(1), contrast: ColorSchemeContrast = .standard
    ) throws -> SearchWindow {
        let dir = try demoDir()
        let opened = Opened()
        let spoken = Spoken()
        let core = try Core.open(dir: dir, key: "", secrets: CoreMemorySecrets(MemorySecrets()))
        let model = SearchModel(core: core, delay: delay, opener: opened.open)
        let view = SearchView(model: model, speak: { spoken.names.append($0) })
            .environment(\._colorSchemeContrast, contrast)
        let hosted = host(AnyView(view), size: NSSize(width: 360, height: 500))
        return SearchWindow(
            hosted: hosted, model: model, core: core, opened: opened, spoken: spoken, dir: dir
        )
    }

    // ask types text into the field and waits until the window shows the answer to all of it
    // with count people, so an answer to a shorter question still on its way cannot land later.
    func ask(_ text: String, people count: Int) async -> Bool {
        hosted.type(text)
        return await waitFor { model.answered == text && model.people.count == count }
    }

    // bars returns the height, in view points from the top, of each person's strength bar, one
    // per row of people in order. The bar's first two points are always filled, so a column
    // through them finds every bar. A bar is four points tall, so a button, which is taller, and
    // the one-point outline of a mark under Increase Contrast are left out.
    func bars(_ rep: NSBitmapImageRep) -> [CGFloat] {
        let scale = CGFloat(rep.pixelsWide) / rep.size.width
        let x = rep.size.width - 12 - 39
        var found: [CGFloat] = []
        var start: Int?
        for row in 0...rep.pixelsHigh {
            let solid = row < rep.pixelsHigh && ink(rep, x: x, y: CGFloat(row) / scale) > 0.5
            if solid, start == nil { start = row }
            if !solid, let top = start {
                let tall = CGFloat(row - top) / scale
                if tall >= 3, tall <= 6 { found.append(CGFloat(top + row) / 2 / scale) }
                start = nil
            }
        }
        return found
    }

    // marked returns the rows, counted from zero, that carry the mark in the rendered window,
    // read in the middle of each row's name line, where only the mark's fill is drawn.
    func marked() throws -> [Int] {
        let rep = try hosted.render()
        let rows = bars(rep).enumerated()
        return rows.filter { ink(rep, x: 200, y: $0.element) > 0.03 }.map(\.offset)
    }

    // edge returns the strongest ink just left of row's name line, where only the mark's outline
    // is drawn, from zero for nothing to one for solid.
    func edge(_ row: Int) throws -> CGFloat {
        let rep = try hosted.render()
        let found = bars(rep)
        try #require(found.count > row, "bars \(found)")
        return stride(from: 0, to: 11, by: 0.5).map { ink(rep, x: $0, y: found[row]) }.max() ?? 0
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
    // SearchViewTests drive the real search window with key presses and read what it draws.
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
            await search.hosted.steady()
            #expect(search.bars(try search.hosted.render()).count == 4)
            #expect(try search.marked() == [0])
            #expect(search.model.enterHint == "Enter messages the first person.")
            let frame = search.hosted.window.frame
            let fitting = search.hosted.view.fittingSize
            let bands = try search.bands()
            search.hosted.press(.down)
            search.hosted.press(.down)
            #expect(search.model.selected == 2)
            #expect(search.model.enterHint == "Enter messages \(names[2]).")
            await search.hosted.steady()
            #expect(try search.marked() == [2])
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
            await search.hosted.steady()
            #expect(search.bars(try search.hosted.render()).isEmpty)
            search.hosted.press(.escape)
            #expect(await waitFor(3) { search.model.detail == nil })
            #expect(search.model.selected == 2)
            await search.hosted.steady()
            #expect(try search.marked() == [2])
            for _ in 0..<4 { search.hosted.press(.up) }
            #expect(search.model.selected == 0)
            await search.hosted.steady()
            #expect(try search.marked() == [0])
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
            search.hosted.expectUntouched()
        }

        // Test 3: A last name alone that answers no one lists the closest people by name under
        // the field, and the window promises nothing for Enter.
        @Test func surnameListsClosestByName() async throws {
            let search = try SearchWindow.open()
            defer { search.close() }
            search.hosted.type("zzqx")
            let none = await waitFor {
                search.model.answered == "zzqx" && search.model.message != nil
            }
            #expect(none)
            await search.hosted.steady()
            let nobody = search.hosted.view.fittingSize.height
            for _ in 0..<4 { search.hosted.press(.delete) }
            search.hosted.type("vance")
            let listed = await waitFor {
                search.model.answered == "vance" && !search.model.closest.isEmpty
            }
            #expect(listed)
            #expect(search.model.people.isEmpty)
            #expect(search.model.closest.map(\.name).contains("Pam Vance"))
            #expect(search.model.message == nil)
            #expect(search.model.enterHint == nil)
            await search.hosted.steady()
            #expect(search.hosted.view.fittingSize.height > nobody + 20)
            search.hosted.expectUntouched()
        }

        // Test 4: Under Increase Contrast the marked person carries an outline and the others do
        // not, and at the standard setting no row does.
        @Test func increasedContrastOutlinesTheMark() async throws {
            let cases: [(ColorSchemeContrast, Bool)] = [(.increased, true), (.standard, false)]
            for (contrast, outlined) in cases {
                let search = try SearchWindow.open(contrast: contrast)
                defer { search.close() }
                #expect(await search.ask("billing kafka", people: 4))
                await search.hosted.steady()
                let marked = try search.edge(0)
                let plain = try search.edge(1)
                #expect((marked > 0.4) == outlined, "\(contrast) marked edge \(marked)")
                #expect(plain < 0.05, "\(contrast) unmarked edge \(plain)")
            }
        }

        // Test 5: Each arrow press that moves the mark hands VoiceOver the newly marked name, and a
        // press at either end hands it nothing.
        @Test func arrowsSpeakEachMove() async throws {
            let search = try SearchWindow.open()
            defer { search.close() }
            #expect(await search.ask("billing kafka", people: 4))
            let names = search.model.people.map(\.name)
            for _ in 0..<4 { search.hosted.press(.down) }
            #expect(search.spoken.names == [names[1], names[2], names[3]])
            for _ in 0..<4 { search.hosted.press(.up) }
            #expect(search.spoken.names == [
                names[1], names[2], names[3], names[2], names[1], names[0],
            ])
            search.hosted.expectUntouched()
        }
    }
}
