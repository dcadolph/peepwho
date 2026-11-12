import Foundation
import Testing

@testable import WhoToAskKit

// repoRoot is the Go module the Swift package sits inside.
let repoRoot = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent()
    .deletingLastPathComponent()
    .deletingLastPathComponent()
    .deletingLastPathComponent()

// demoDir writes the simulated company to a fresh directory with the command line's demo
// --save-index, the same index a user gets, and returns the directory.
func demoDir() throws -> URL {
    let dir = FileManager.default.temporaryDirectory
        .appendingPathComponent("whotoask-search-\(UUID().uuidString)", isDirectory: true)
    let go = Process()
    go.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    go.arguments = ["go", "run", ".", "demo", "--save-index", dir.path]
    go.currentDirectoryURL = repoRoot
    go.standardOutput = FileHandle.nullDevice
    go.standardError = FileHandle.nullDevice
    try go.run()
    go.waitUntilExit()
    try #require(go.terminationStatus == 0, "demo --save-index exited \(go.terminationStatus)")
    return dir
}

// Opened records every link the model asks to open.
@MainActor
final class Opened {
    // urls are the links opened, in order.
    var urls: [URL] = []
    // result is what the opener reports back.
    var result = true

    // open records url and reports result.
    func open(_ url: URL) -> Bool {
        urls.append(url)
        return result
    }
}

// SearchTests drive the menu bar search over the demo index through the real core.
@MainActor
struct SearchTests {
    // Test 0: Typing terraform lists Carol Lee first and Enter opens her Slack DM.
    @Test func enterOpensFirstPersonsDM() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: opened.open)
        model.query = "terraform"
        await model.ask(model.query)
        let first = try #require(model.people.first)
        #expect(first.name == "Carol Lee")
        let want = try #require(try await core.person(first.id).links?.first)
        await model.submit()
        let url = try #require(opened.urls.first)
        #expect(opened.urls.count == 1)
        #expect(url.absoluteString == want)
        #expect(url.absoluteString.hasPrefix("slack://user?team="))
        #expect(model.detail == nil)
    }

    // Test 1: Enter before the debounced question runs answers the field first.
    @Test func enterAnswersStaleField() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let model = SearchModel(core: try Core.open(dir: dir, key: "", secrets: KeychainSecrets()),
                                delay: .seconds(60), opener: opened.open)
        model.query = "terraform"
        model.typed(model.query)
        await model.submit()
        #expect(model.people.first?.name == "Carol Lee")
        #expect(opened.urls.count == 1)
    }

    // Test 2: A link that does not open shows the person instead.
    @Test func failedOpenShowsPerson() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        opened.result = false
        let model = SearchModel(core: try Core.open(dir: dir, key: "", secrets: KeychainSecrets()),
                                opener: opened.open)
        model.query = "terraform"
        await model.submit()
        #expect(opened.urls.count == 1)
        #expect(model.detail?.name == "Carol Lee")
        model.back()
        #expect(model.detail == nil)
    }

    // Test 3: An index not built yet says so and opens nothing.
    @Test func emptyIndexOpensNothing() async throws {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("whotoask-empty-\(UUID().uuidString)", isDirectory: true)
        let opened = Opened()
        let model = SearchModel(core: try Core.open(dir: dir, key: "", secrets: KeychainSecrets()),
                                opener: opened.open)
        model.query = "terraform"
        await model.submit()
        #expect(model.people.isEmpty)
        #expect(opened.urls.isEmpty)
        #expect(model.message == SearchModel.noIndex)
    }

    // Test 4: A model with no core shows why and asks nothing.
    @Test func noCore() async {
        let opened = Opened()
        let model = SearchModel(core: nil, openError: "Index key: wrong key.", opener: opened.open)
        model.query = "terraform"
        await model.submit()
        #expect(model.message == "Index key: wrong key.")
        #expect(opened.urls.isEmpty)
    }

    // Test 5: Enter on a person shown from the results opens that person, not the first one.
    // Deploy ranks Grace Kim first and Carol Lee, the one with a Slack link, below her. The footer
    // offers Enter only when there is someone for it to reach.
    @Test func enterOpensShownPerson() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: opened.open)
        #expect(model.enterHint == nil)
        model.query = "zzqx"
        await model.ask(model.query)
        try #require(model.people.isEmpty)
        #expect(model.enterHint == nil)
        model.query = "deploy"
        await model.ask(model.query)
        try #require(model.people.first?.name != "Carol Lee")
        let carol = try #require(model.people.first { $0.name == "Carol Lee" })
        let want = try #require(try await core.person(carol.id).links?.first)
        #expect(model.enterHint == "Enter messages the first person.")
        await model.select(carol.id)
        #expect(model.enterHint == "Enter messages this person.")
        await model.submit()
        #expect(opened.urls.map(\.absoluteString) == [want])
        #expect(model.detail?.id == carol.id)
    }

    // Test 6: The arrow keys mark a row and stop at both ends, Enter opens the marked person and
    // not the first, the hint names whom Enter reaches and whether it messages or shows them, a
    // field not answered yet promises neither, and a new answer marks the first row again.
    // Billing kafka ranks four people, and only the fourth, Pam Vance, has no link.
    @Test func arrowsMarkWhoEnterOpens() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: opened.open)
        #expect(model.selected == nil)
        model.moveDown()
        #expect(model.selected == nil)
        model.query = "billing kafka"
        await model.ask(model.query)
        try #require(model.people.count == 4)
        #expect(model.selected == 0)
        model.moveUp()
        #expect(model.selected == 0)
        #expect(model.enterHint == "Enter messages the first person.")
        model.moveDown()
        model.moveDown()
        #expect(model.selected == 2)
        let third = model.people[2]
        #expect(model.enterHint == "Enter messages \(third.name).")
        let want = try #require(try await core.person(third.id).links?.first)
        let first = try #require(try await core.person(model.people[0].id).links?.first)
        try #require(want != first)
        await model.submit()
        #expect(opened.urls.map(\.absoluteString) == [want])
        #expect(model.detail == nil)
        model.moveDown()
        model.moveDown()
        #expect(model.selected == 3)
        let last = model.people[3]
        #expect(try await core.person(last.id).links?.isEmpty ?? true)
        #expect(!last.isReachable)
        #expect(model.enterHint == "Enter shows \(last.name).")
        model.moveUp()
        #expect(model.selected == 2)
        model.query = "terraform"
        #expect(model.enterHint == "Enter opens the first person.")
        await model.ask(model.query)
        #expect(model.selected == 0)
        #expect(model.enterHint == "Enter messages the first person.")
    }

    // Test 7: Command-Return shows the marked person, the arrows leave the mark alone on a person
    // page, Escape there goes back to the results with the same row marked, and Escape on the
    // results changes nothing.
    @Test func escapeLeavesPerson() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: opened.open)
        model.query = "billing"
        await model.ask(model.query)
        try #require(model.people.count == 3)
        model.moveDown()
        let second = model.people[1]
        await model.showMarked()
        #expect(model.detail?.id == second.id)
        model.moveDown()
        #expect(model.selected == 1)
        #expect(model.escape())
        #expect(model.detail == nil)
        #expect(model.selected == 1)
        #expect(!model.escape())
        #expect(model.detail == nil)
        #expect(model.selected == 1)
        #expect(opened.urls.isEmpty)
    }

    // Test 8: Command-Return before the debounced question runs answers the field first and shows
    // the marked person from that answer, and the question that was waiting never closes the page.
    @Test func commandReturnAnswersStaleField() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let model = SearchModel(core: try Core.open(dir: dir, key: "", secrets: KeychainSecrets()),
                                delay: .milliseconds(200), opener: opened.open)
        model.query = "billing"
        await model.ask(model.query)
        model.moveDown()
        try #require(model.selected == 1)
        model.query = "terraform"
        model.typed(model.query)
        await model.showMarked()
        #expect(model.people.first?.name == "Carol Lee")
        #expect(model.selected == 0)
        #expect(model.detail?.name == "Carol Lee")
        try await Task.sleep(for: .milliseconds(500))
        #expect(model.detail?.name == "Carol Lee")
        #expect(opened.urls.isEmpty)
    }

    // Test 9: Clicking a row shows that person and marks the row, so after Escape the hint and
    // Enter reach the person clicked, and a person not in the results leaves the mark alone.
    // Billing kafka ranks four people, and the third has a link the first does not share.
    @Test func clickMovesMark() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: opened.open)
        model.query = "billing kafka"
        await model.ask(model.query)
        try #require(model.people.count == 4)
        let third = model.people[2]
        await model.select(third.id)
        #expect(model.detail?.id == third.id)
        #expect(model.selected == 2)
        #expect(model.escape())
        #expect(model.enterHint == "Enter messages \(third.name).")
        let want = try #require(try await core.person(third.id).links?.first)
        await model.submit()
        #expect(opened.urls.map(\.absoluteString) == [want])
        await model.select("nobody-in-these-results")
        #expect(model.selected == 2)
    }

    // Test 10: An arrow key returns the person it marked, which the window speaks, and nothing at
    // either end, on a person page, or with no results, and the strength bar reads as a whole
    // percent clamped to the bar.
    @Test func movesSayWhoIsMarked() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let model = SearchModel(core: try Core.open(dir: dir, key: "", secrets: KeychainSecrets()),
                                delay: .milliseconds(1), opener: Opened().open)
        #expect(model.moveDown() == nil)
        model.query = "billing kafka"
        await model.ask(model.query)
        try #require(model.people.count == 4)
        #expect(model.moveUp() == nil)
        #expect(model.moveDown()?.id == model.people[1].id)
        #expect(model.moveDown()?.id == model.people[2].id)
        #expect(model.moveDown()?.id == model.people[3].id)
        #expect(model.moveDown() == nil)
        #expect(model.selected == 3)
        #expect(model.moveUp()?.id == model.people[2].id)
        await model.showMarked()
        try #require(model.detail != nil)
        #expect(model.moveUp() == nil)
        #expect(model.selected == 2)
        let values = [0.834, 1, 0, 1.4, -0.2].map(SearchModel.strengthValue)
        #expect(values == ["83 percent", "100 percent", "0 percent", "100 percent", "0 percent"])
    }

    // Test 11: A surname alone, which the answer leaves to the name search, lists the people it
    // names under Closest by name, a row opens that person, Enter promises nothing, a word nobody
    // holds still says no one matches, and a new core clears the list.
    @Test func surnameFallsBackToClosest() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, delay: .milliseconds(1), opener: Opened().open)
        model.query = "vance"
        await model.ask(model.query)
        #expect(model.people.isEmpty)
        #expect(model.message == nil)
        let first = try #require(model.closest.first)
        #expect(first.name == "Pam Vance")
        #expect(model.closest.allSatisfy { $0.kind == "person" })
        #expect(model.closest.count <= 5)
        #expect(model.enterHint == nil)
        await model.select(first.id)
        #expect(model.detail?.name == "Pam Vance")
        model.query = "zzqx"
        await model.ask(model.query)
        #expect(model.closest.isEmpty)
        #expect(model.message == "No one matches \"zzqx\" yet.")
        model.query = "vance"
        await model.ask(model.query)
        try #require(!model.closest.isEmpty)
        model.use(core)
        #expect(model.closest.isEmpty)
    }
}

// SearchStateTests check that the window never shows or opens what it moved on from.
@MainActor
struct SearchStateTests {
    // Test 0: Core errors read as sentences with no internal prefix and no container path.
    @Test func describeCleansCoreErrors() {
        let dir = URL(fileURLWithPath: "/Users/x/Library/Containers/com.dcadolph.whotoask/Data/whotoask")
        func text(_ message: String) -> String {
            SearchModel.describe(NSError(domain: "go", code: 1, userInfo: [NSLocalizedDescriptionKey: message]), dataDir: dir)
        }
        #expect(text("core: unknown person: a@b.c") == "Unknown person: a@b.c.")
        #expect(text("core: open \(dir.path)/index.json: boom") == "Open index.json: boom.")
        #expect(text("core: \(dir.path) is gone") == "The data folder is gone.")
        #expect(text("core: no index yet: index a source first") == SearchModel.noIndex)
        #expect(text("Already a sentence.") == "Already a sentence.")
        #expect(text("") == "Something went wrong.")
    }

    // Test 1: A new core drops the field, what the old one answered, its changes, and any
    // question waiting.
    @Test func useClearsEverything() async throws {
        let dir = tempDir("whotoask-data")
        let csvDir = tempDir("whotoask-csv")
        try FileManager.default.createDirectory(at: csvDir, withIntermediateDirectories: true)
        defer {
            try? FileManager.default.removeItem(at: dir)
            try? FileManager.default.removeItem(at: csvDir)
        }
        let csv = csvDir.appendingPathComponent("people.csv")
        let header = "name,email,title,team,topics\n"
        let jane = "Jane Doe,jane@x.com,Engineer,Billing,retries\n"
        try (header + jane).write(to: csv, atomically: true, encoding: .utf8)
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let start = Date()
        _ = try await core.index("org-csv", flags: ["--file", csv.path], now: start)
        try (header + jane + "Ann Lee,ann@x.com,Engineer,Payments,ledger\n").write(to: csv, atomically: true, encoding: .utf8)
        _ = try await core.index("org-csv", flags: ["--file", csv.path], now: start.addingTimeInterval(10))

        let defaults = try #require(UserDefaults(suiteName: "whotoask-test-\(UUID().uuidString)"))
        defaults.set(start.addingTimeInterval(1), forKey: SearchModel.lastLookKey)
        let model = SearchModel(core: core, delay: .milliseconds(200), defaults: defaults) { _ in true }
        await model.opened(now: start.addingTimeInterval(11))
        try #require(!model.changes.isEmpty)
        await model.ask("ledger")
        try #require(!model.people.isEmpty)
        try #require(model.selected == 0)

        model.query = "retries"
        model.typed("retries")
        model.use(core)
        #expect(model.query.isEmpty)
        #expect(model.people.isEmpty)
        #expect(model.selected == nil)
        #expect(model.changes.isEmpty)
        try await Task.sleep(for: .milliseconds(500))
        #expect(model.people.isEmpty)
    }

    // Test 2: Going back during a lookup, or swapping the core, leaves the window as it is and
    // opens nothing.
    @Test func lateLookupIsDropped() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let opened = Opened()
        let model = SearchModel(core: core, opener: opened.open)
        await model.ask("terraform")
        let id = try #require(model.people.first?.id)

        // A task on the main actor runs to its lookup before the yield returns, so the lookup is
        // running when the window moves on, however busy the machine is. The answer cannot land
        // before then, since it needs the main actor too.
        let shown = Task { await model.select(id) }
        await Task.yield()
        model.back()
        await shown.value
        #expect(model.detail == nil)

        let opening = Task { await model.open(id) }
        await Task.yield()
        model.use(core)
        await opening.value
        #expect(opened.urls.isEmpty)

        await model.select(id)
        #expect(model.detail != nil)
    }

    // Test 3: A changes read that fails starts no clock and moves no last look, and the next open
    // that reads shows what happened in between.
    @Test func failedReadKeepsLastLook() async throws {
        let dir = tempDir("whotoask-data")
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let changes = dir.appendingPathComponent("changes.json")
        try "{".write(to: changes, atomically: true, encoding: .utf8)
        let defaults = try #require(UserDefaults(suiteName: "whotoask-test-\(UUID().uuidString)"))
        let model = SearchModel(core: core, defaults: defaults) { _ in true }

        await model.opened()
        model.closed()
        #expect(defaults.object(forKey: SearchModel.lastLookKey) == nil)

        let last = Date(timeIntervalSince1970: 1000)
        defaults.set(last, forKey: SearchModel.lastLookKey)
        await model.opened()
        model.closed()
        #expect(defaults.object(forKey: SearchModel.lastLookKey) as? Date == last)

        try FileManager.default.removeItem(at: changes)
        await model.opened()
        model.closed(now: last.addingTimeInterval(5))
        #expect(defaults.object(forKey: SearchModel.lastLookKey) as? Date == last.addingTimeInterval(5))
    }
}

// LinkTests check that the app opens only the link schemes it allows, since links come from
// servers and Enter opens without asking.
@MainActor
struct LinkTests {
    // Test 0: Only https, http, slack, and mailto reach the opener, in any letter case.
    @Test(arguments: [
        ("https://x.example/a", true),
        ("HTTPS://x.example/a", true),
        ("http://x.example/a", true),
        ("slack://user?team=T1&id=U1", true),
        ("mailto:a@x.example", true),
        ("file:///etc/passwd", false),
        ("FILE:///etc/passwd", false),
        ("javascript:alert(1)", false),
        ("x-custom-app://run?cmd=1", false),
        ("ssh://x.example", false),
        ("no scheme here", false),
        ("", false),
    ])
    func openLinkAllowsOnlySafeSchemes(link: String, allowed: Bool) {
        let opened = Opened()
        let model = SearchModel(core: nil, opener: opened.open)
        model.openLink(link)
        #expect(opened.urls.count == (allowed ? 1 : 0))
        #expect(model.message == (allowed ? nil : "Could not open \(link)."))
    }

    // Test 1: Enter on a person whose only link is refused opens nothing and shows the person.
    @Test func enterRefusesUnsafeLink() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try editIndex(dir) { $0.replacingOccurrences(of: "\"slack://user?", with: "\"file:///etc/passwd?") }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, opener: opened.open)
        model.query = "terraform"
        await model.submit()
        let first = try #require(model.people.first)
        #expect(try await core.person(first.id).links?.first?.hasPrefix("file:") == true)
        #expect(opened.urls.isEmpty)
        #expect(model.detail?.id == first.id)
    }

    // Test 2: Enter skips a refused link and opens the first allowed one.
    @Test func enterSkipsToAllowedLink() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try editIndex(dir) { $0.replacingOccurrences(of: "\"slack://user?", with: "\"x-custom-app://run\",\"slack://user?") }
        let opened = Opened()
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let model = SearchModel(core: core, opener: opened.open)
        model.query = "terraform"
        await model.submit()
        let first = try #require(model.people.first)
        #expect(try await core.person(first.id).links?.first?.hasPrefix("x-custom-app:") == true)
        #expect(opened.urls.count == 1)
        #expect(opened.urls.first?.scheme == "slack")
        #expect(model.detail == nil)
    }

    // editIndex rewrites the plain index in dir through change, and fails if change does nothing.
    private func editIndex(_ dir: URL, _ change: (String) -> String) throws {
        let file = dir.appendingPathComponent("index.json")
        let before = try String(contentsOf: file, encoding: .utf8)
        let after = change(before)
        try #require(after != before, "the index holds no link to edit")
        try after.write(to: file, atomically: true, encoding: .utf8)
    }
}

// ConcurrencyTests check that a question never waits behind an index run.
struct ConcurrencyTests {
    // Test 0: An ask started while an index run is held at its first progress line finishes
    // before the run does. Holding the run, rather than sleeping and hoping it is still going,
    // keeps a fast disk from finishing the index first.
    @Test func askDuringIndex() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let order = Order()
        let started = DispatchSemaphore(value: 0)
        let release = DispatchSemaphore(value: 0)
        let first = Once()
        core.observeProgress { _ in
            guard first.take() else { return }
            started.signal()
            _ = release.wait(timeout: .now() + 10)
        }
        let index = Task {
            _ = try await core.index("git", flags: ["--repo-path", repoRoot.path, "--git-since-days", "10000"])
            await order.add("index")
        }
        let running = await withCheckedContinuation { cont in
            DispatchQueue.global().async { cont.resume(returning: started.wait(timeout: .now() + 10)) }
        }
        try #require(running == .success, "the index run never logged a line")
        let answer = try await core.ask("terraform")
        await order.add("ask")
        release.signal()
        try await index.value
        #expect(answer.people?.first?.name == "Carol Lee")
        #expect(await order.names == ["ask", "index"])
    }
}

// Order records the order tasks finish in.
actor Order {
    // names are the finished tasks, first first.
    var names: [String] = []

    // add records name as finished.
    func add(_ name: String) { names.append(name) }
}

// Once is true the first time take is called and false after, from any thread.
final class Once: @unchecked Sendable {
    // lock guards taken.
    private let lock = NSLock()
    // taken is true once take has returned true.
    private var taken = false

    // take reports whether this is the first call.
    func take() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if taken { return false }
        taken = true
        return true
    }
}
