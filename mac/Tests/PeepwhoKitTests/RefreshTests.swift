import Foundation
import Testing

@testable import PeepwhoKit

// RefreshTests check the stale refresh rule and the changes since the last look through the
// real core.
@MainActor
struct RefreshTests {
    // Test 0: Change lines name a few and count the rest.
    @Test func summary() throws {
        let raw = #"{"people_joined":["A","B","C","D","E"],"teams_removed":["Payments"],"channels_added":["infra"]}"#
        let c: IndexChanges = try Core.decode(raw)
        #expect(ChangeSummary.lines(c) == ["Joined: A, B, C and 2 more.", "Teams gone: Payments.", "New channels: #infra."])
        let none: IndexChanges = try Core.decode("{}")
        #expect(ChangeSummary.lines(none).isEmpty)
    }

    // Test 1: An index older than the interval refreshes once, a second call inside the
    // interval does not, and a long interval never refreshes a fresh index.
    @Test func refreshIfStale() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        defaults.set(60.0, forKey: SettingsModel.intervalKey)
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        #expect(await !model.refreshIfStale())
        await model.pick(repo, as: .git)
        #expect(model.refreshInterval == 60)
        #expect(await !model.refreshIfStale())
        let later = Date().addingTimeInterval(120)
        #expect(await model.refreshIfStale(now: later))
        #expect(model.lastRun?.kind == .refreshedOnItsOwn)
        let onItsOwn = try #require(model.lastRun).line
        #expect(onItsOwn.hasPrefix("Refreshed on its own at "))
        #expect(model.lines == [onItsOwn, refreshedOneRepo(model)])
        #expect(await !model.refreshIfStale(now: later.addingTimeInterval(30)))

        defaults.set(86400.0, forKey: SettingsModel.intervalKey)
        let fresh = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        #expect(await !fresh.refreshIfStale(now: Date().addingTimeInterval(120)))
        defaults.removeObject(forKey: SettingsModel.intervalKey)
        #expect(fresh.refreshInterval == 6 * 3600)
    }

    // Test 2: A person added between two index runs shows as joined at the next open, and not
    // again after the menu closes.
    @Test func changesSinceLastLook() async throws {
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

        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SearchModel(core: core, defaults: defaults) { _ in true }
        await model.opened(now: start.addingTimeInterval(1))
        #expect(model.changes.isEmpty)
        model.closed(now: start.addingTimeInterval(2))

        try (header + jane + "Ann Lee,ann@x.com,Engineer,Payments,ledger\n").write(to: csv, atomically: true, encoding: .utf8)
        _ = try await core.index("org-csv", flags: ["--file", csv.path], now: start.addingTimeInterval(10))
        await model.opened(now: start.addingTimeInterval(11))
        #expect(model.changes == ["Joined: Ann Lee.", "New teams: Payments."])
        model.closed(now: start.addingTimeInterval(12))
        await model.opened(now: start.addingTimeInterval(13))
        #expect(model.changes.isEmpty)
    }
}
