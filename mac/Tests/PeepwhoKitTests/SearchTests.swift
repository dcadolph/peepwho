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
        #expect(model.message?.first?.isUppercase == true)
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
