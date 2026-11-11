import Foundation
import Testing

@testable import WhoToAskKit

// sampleCompany builds the sample company the way the app build does, once per test run.
let sampleCompany: Result<URL, Error> = Result {
    let dir = tempDir("whotoask-sample")
    let go = Process()
    go.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    go.arguments = ["go", "run", ".", "demo", "--big", "--save-index", dir.path]
    go.currentDirectoryURL = repoRoot
    go.standardOutput = FileHandle.nullDevice
    go.standardError = FileHandle.nullDevice
    try go.run()
    go.waitUntilExit()
    try #require(go.terminationStatus == 0, "go run demo exited \(go.terminationStatus)")
    return dir
}

// SampleTests load the simulated company through the real core and use every surface on it.
@MainActor
struct SampleTests {
    // model returns settings over dir that bundle the sample company.
    private func model(_ dir: URL, places: PlaceStore = MemoryPlaces()) throws -> SettingsModel {
        let defaults = try #require(UserDefaults(suiteName: "whotoask-test-\(UUID().uuidString)"))
        return SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                             secrets: MemorySecrets(), places: places, defaults: defaults,
                             sampleDir: try sampleCompany.get())
    }

    // Test 0: A person loads the company from an empty index, it reads as sample data, every
    // planted question finds its known person first, nothing can index or refresh into it, and a
    // wipe takes it all.
    @Test func loadUseWipe() async throws {
        let dir = tempDir("whotoask-data")
        defer { try? FileManager.default.removeItem(at: dir) }
        let settings = try model(dir)
        var heard: [Bool] = []
        var empty: [Bool] = []
        settings.onSample = { heard.append($0) }
        settings.onEmpty = { empty.append($0) }
        await settings.loadStatus()
        #expect(empty == [true])
        #expect(settings.canLoadSample)
        #expect(settings.summary.hasSuffix("or load the sample company to look around."))

        await settings.loadSample()
        #expect(settings.lines == ["Loaded the sample company: 220 people from 8 sources. Clear Index removes it."])
        let status = try #require(settings.status)
        #expect(status.sample)
        #expect(status.people == 220)
        #expect(status.deactivated == 1)
        #expect(settings.canLoadSample)
        #expect(settings.summary.hasPrefix("Sample company, not real people. 220 people including 1 deactivated,"))
        #expect(settings.summary.contains("Loaded just now."))
        #expect(heard.last == true)
        #expect(empty.last == false)
        #expect(settings.sourceAges.count == 8)
        #expect(settings.sourceAges.allSatisfy { $0.detail == "Loaded just now." })

        let search = SearchModel(core: settings.core, opener: { _ in true })
        let known = [
            ("terraform", "erin.hale79@example.com"),
            ("billing keeps breaking for some customers", "stanley.lewis7@example.com"),
            ("kafka is falling behind again", "stanley.lewis7@example.com"),
            ("kubernetes rollouts keep stalling", "walter.bream39@example.com"),
            ("payroll taxes is acting up again", "michelle.schrute22@example.com"),
        ]
        for (question, person) in known {
            await search.ask(question)
            #expect(search.people.first?.id == person, "\(question)")
        }
        await search.select("erin.hale79@example.com")
        #expect(search.detail?.name == "Erin Hale")

        let changes = try await settings.core?.changes(since: Date(timeIntervalSince1970: 0))
        #expect(changes?.changes.peopleJoined?.count == 220)

        let weekLater = Date().addingTimeInterval(7 * 86400)
        #expect(await settings.refreshIfStale(now: weekLater) == false)
        await settings.refresh()
        #expect(settings.lines.first?.contains("holds sample data") == true)

        await settings.clearIndex()
        #expect(!FileManager.default.fileExists(atPath: dir.path))
        #expect(settings.status?.sample == false)
        #expect(settings.canLoadSample)
        #expect(heard.last == false)
        #expect(empty.last == true)
    }

    // Test 1: The control is gone once real data is indexed, and a load is refused anyway.
    @Test func realIndexKeepsItsPlace() async throws {
        let repo = try gitRepo()
        let dir = tempDir("whotoask-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let settings = try model(dir)
        await settings.pick(repo, as: .git)
        #expect(settings.status?.sample == false)
        #expect(!settings.canLoadSample)
        let before = settings.status?.people
        await settings.loadSample()
        #expect(settings.lines.first?.contains("holds your own data") == true)
        #expect(settings.status?.people == before)
    }

    // Test 2: With no bundled company there is no control and no mention of one.
    @Test func noBundledCompany() async throws {
        let dir = tempDir("whotoask-data")
        let defaults = try #require(UserDefaults(suiteName: "whotoask-test-\(UUID().uuidString)"))
        let settings = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                     secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await settings.loadStatus()
        #expect(!settings.canLoadSample)
        #expect(settings.summary == "Nothing indexed yet. Pick a folder or add a token, then Index Now.")
    }
}
