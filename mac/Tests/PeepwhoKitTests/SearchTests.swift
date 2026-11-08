import Foundation
import Testing

@testable import PeepwhoKit

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
        .appendingPathComponent("peepwho-search-\(UUID().uuidString)", isDirectory: true)
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
            .appendingPathComponent("peepwho-empty-\(UUID().uuidString)", isDirectory: true)
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
}

// SearchStateTests check that the window never shows or opens what it moved on from.
@MainActor
struct SearchStateTests {
    // Test 0: Core errors read as sentences with no internal prefix and no container path.
    @Test func describeCleansCoreErrors() {
        let dir = URL(fileURLWithPath: "/Users/x/Library/Containers/com.dcadolph.peepwho/Data/peepwho")
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

    // Test 1: A new core drops what the old one answered, its changes, and any question waiting.
    @Test func useClearsEverything() async throws {
        let dir = tempDir("peepwho-data")
        let csvDir = tempDir("peepwho-csv")
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

        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        defaults.set(start.addingTimeInterval(1), forKey: SearchModel.lastLookKey)
        let model = SearchModel(core: core, delay: .milliseconds(200), defaults: defaults) { _ in true }
        await model.opened(now: start.addingTimeInterval(11))
        try #require(!model.changes.isEmpty)
        await model.ask("ledger")
        try #require(!model.people.isEmpty)

        model.typed("retries")
        model.use(core)
        #expect(model.people.isEmpty)
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

        async let shown: Void = model.select(id)
        await Task.yield()
        model.back()
        await shown
        #expect(model.detail == nil)

        async let opening: Void = model.open(id)
        await Task.yield()
        model.use(core)
        await opening
        #expect(opened.urls.isEmpty)

        await model.select(id)
        #expect(model.detail != nil)
    }

    // Test 3: A changes read that fails starts no clock and moves no last look, and the next open
    // that reads shows what happened in between.
    @Test func failedReadKeepsLastLook() async throws {
        let dir = tempDir("peepwho-data")
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let changes = dir.appendingPathComponent("changes.json")
        try "{".write(to: changes, atomically: true, encoding: .utf8)
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
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

// ConcurrencyTests check that a question never waits behind an index run.
struct ConcurrencyTests {
    // Test 0: An ask started after a long index run starts finishes before it.
    @Test func askDuringIndex() async throws {
        let dir = try demoDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let core = try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        let order = Order()
        let index = Task {
            _ = try await core.index("git", flags: ["--repo-path", repoRoot.path, "--git-since-days", "10000"])
            await order.add("index")
        }
        try await Task.sleep(for: .milliseconds(100))
        let answer = try await core.ask("terraform")
        await order.add("ask")
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
