import Foundation
import Security
import Testing

@testable import PeepwhoKit

// MemorySecrets keeps secrets in a dictionary.
final class MemorySecrets: SecretStore {
    // values are the stored secrets by name.
    var values: [String: String] = [:]

    // get returns the value stored under name.
    func get(_ name: String) -> String? { values[name] }

    // set stores value under name.
    func set(_ name: String, _ value: String) throws { values[name] = value }

    // delete removes the value stored under name.
    func delete(_ name: String) { values[name] = nil }
}

// MemoryPlaces keeps places in a dictionary and counts access, so a test sees every resolve
// matched by a release.
final class MemoryPlaces: PlaceStore {
    // urls are the saved places by slot.
    var urls: [BookmarkSlot: URL] = [:]
    // open is how many resolves have no release yet.
    var open = 0
    // resolves is how many resolves ran.
    var resolves = 0

    // save stores url for slot.
    func save(_ url: URL, in slot: BookmarkSlot) throws { urls[slot] = url }

    // resolve returns slot's place and counts the access.
    func resolve(_ slot: BookmarkSlot) throws -> URL? {
        guard let url = urls[slot] else { return nil }
        open += 1
        resolves += 1
        return url
    }

    // release ends one access.
    func release(_ url: URL) { open -= 1 }

    // forget drops slot's place.
    func forget(_ slot: BookmarkSlot) { urls[slot] = nil }

    // has reports whether slot holds a place.
    func has(_ slot: BookmarkSlot) -> Bool { urls[slot] != nil }
}

// tempDir returns a fresh directory path under the temporary directory, not yet created.
func tempDir(_ prefix: String) -> URL {
    FileManager.default.temporaryDirectory.appendingPathComponent("\(prefix)-\(UUID().uuidString)", isDirectory: true)
}

// gitRepo makes a repository with one commit by one author and returns its folder.
func gitRepo() throws -> URL {
    let dir = tempDir("peepwho-repo")
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    try "package main\n".write(to: dir.appendingPathComponent("main.go"), atomically: true, encoding: .utf8)
    for args in [["init", "-q"], ["add", "main.go"], ["commit", "-q", "-m", "Add main"]] {
        let git = Process()
        git.executableURL = URL(fileURLWithPath: "/usr/bin/git")
        git.arguments = ["-c", "user.name=Jane Doe", "-c", "user.email=jane@example.com"] + args
        git.currentDirectoryURL = dir
        git.standardOutput = FileHandle.nullDevice
        git.standardError = FileHandle.nullDevice
        try git.run()
        git.waitUntilExit()
        try #require(git.terminationStatus == 0, "git \(args) exited \(git.terminationStatus)")
    }
    return dir
}

// SettingsTests check the flags each source runs with and the Index, Refresh, and Wipe actions
// through the real core.
@MainActor
struct SettingsTests {
    // Test 0: Token sources run only with their secrets stored and their scope filled in.
    @Test func tokenJobs() {
        var scopes = Scopes()
        scopes.githubRepos = "o/a, o/b,"
        scopes.githubOrg = "ignored"
        scopes.jiraProjects = "KAFKA"
        scopes.jiraServer = true
        scopes.confluenceSpaces = "ENG"
        let all: Set<String> = [
            SecretName.slackToken, SecretName.githubToken, SecretName.jiraURL, SecretName.jiraToken,
            SecretName.confluenceURL, SecretName.confluenceToken, SecretName.pagerdutyToken,
        ]
        let got = IndexJob.forTokens(scopes) { all.contains($0) }
        #expect(got == [
            IndexJob(source: "slack", flags: []),
            IndexJob(source: "github", flags: ["--repo", "o/a", "--repo", "o/b"]),
            IndexJob(source: "jira", flags: ["--jira-server", "--jira-project", "KAFKA"]),
            IndexJob(source: "confluence", flags: ["--confluence-space", "ENG"]),
            IndexJob(source: "pagerduty", flags: []),
        ])
        #expect(IndexJob.forTokens(Scopes()) { all.contains($0) }.map(\.source) == ["slack", "pagerduty"])
        #expect(IndexJob.forTokens(scopes) { _ in false }.isEmpty)
        scopes.githubRepos = ""
        #expect(IndexJob.forTokens(scopes) { $0 == SecretName.githubToken }
            == [IndexJob(source: "github", flags: ["--github-org", "ignored"])])
    }

    // Test 1: Ages read in plain words with no "just now ago".
    @Test func ages() {
        #expect(Age.words(0) == "just now")
        #expect(Age.words(59) == "just now")
        #expect(Age.words(60) == "1 minute ago")
        #expect(Age.words(7200) == "2 hours ago")
        #expect(Age.words(86400) == "1 day ago")
        #expect(Age.words(3 * 86400 + 5) == "3 days ago")
    }

    // Test 2: A saved token is stored, an empty one deletes it, and scopes persist.
    @Test func secretsAndScopes() throws {
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let secrets = MemorySecrets()
        let model = SettingsModel(dir: tempDir("peepwho-data"), open: { try Core.open(dir: tempDir("x"), key: "", secrets: KeychainSecrets()) },
                                  secrets: secrets, places: MemoryPlaces(), defaults: defaults)
        model.saveSecret(SecretName.slackToken, "  xoxb-1  ")
        #expect(secrets.values[SecretName.slackToken] == "xoxb-1")
        #expect(model.has(SecretName.slackToken))
        model.saveSecret(SecretName.slackToken, "")
        #expect(!model.has(SecretName.slackToken))
        model.scopes.jiraProjects = "KAFKA"
        #expect(Scopes.load(defaults).jiraProjects == "KAFKA")
    }

    // Test 3: A picked repo indexes, refreshes with access held, and a wipe leaves no directory
    // and hands the search a fresh, empty core.
    @Test func indexRefreshWipe() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let places = MemoryPlaces()
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        var handed: [Core?] = []
        model.onCore = { handed.append($0) }

        await model.pick(repo, as: .git)
        #expect(model.lines == ["git: indexed repo:\(repo.path)."])
        #expect(model.placeNames[.git] == repo.lastPathComponent)
        #expect((model.status?.people ?? 0) > 0)
        #expect(model.status?.sources["git"] != nil)

        await model.refresh()
        #expect(model.lines == ["git: refreshed repo:\(repo.path)."])

        await model.indexAll()
        #expect(model.lines == ["git: indexed repo:\(repo.path)."])
        #expect(places.open == 0)
        #expect(places.resolves > 3)

        await model.wipe()
        #expect(model.lines.first?.hasPrefix("Deleted ") == true)
        #expect(!FileManager.default.fileExists(atPath: dir.path))
        #expect(model.status?.people == 0)
        #expect(handed.count == 2)
        #expect(handed.first! == nil)
        #expect(handed.last! != nil)
    }

    // Test 4: Indexing with nothing set up says what to do.
    @Test func nothingToIndex() async throws {
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let dir = tempDir("peepwho-data")
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await model.indexAll()
        #expect(model.lines == ["Nothing to index yet. Pick a folder or add a token first."])
        await model.refresh()
        #expect(model.lines == ["Nothing to refresh yet. Index something first."])
    }

    // Test 6: A wipe or a second index started while an index runs is refused, so the running
    // index cannot write its files back after the wipe.
    @Test func actionsNeverOverlap() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let places = MemoryPlaces()
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        async let picked: Void = model.pick(repo, as: .git)
        await Task.yield()
        #expect(model.busy != nil)
        await model.wipe()
        #expect(model.lines.first?.hasSuffix("is still running. Try again when it finishes.") == true)
        await model.indexAll()
        #expect(model.lines.first?.hasSuffix("is still running. Try again when it finishes.") == true)
        await picked
        #expect(model.busy == nil)
        #expect(model.status?.sources["git"] != nil)
        #expect(FileManager.default.fileExists(atPath: dir.path))
        #expect(places.open == 0)
    }

    // Test 5: An open that failed, such as under a locked Keychain, opens on the next try, hands
    // the search the core, and clears the failure.
    @Test func reopenAfterFailure() throws {
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let dir = tempDir("peepwho-data")
        var locked = true
        let model = SettingsModel(dir: dir, open: {
            if locked { throw KeychainError(status: errSecInteractionNotAllowed) }
            return try Core.open(dir: dir, key: "", secrets: KeychainSecrets())
        }, secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        var handed: [Core?] = []
        model.onCore = { handed.append($0) }
        #expect(model.core == nil)
        #expect(model.lines.first?.hasPrefix("The index did not open") == true)

        model.reopenIfClosed()
        #expect(model.core == nil)
        #expect(model.lines.first?.hasPrefix("The index did not open") == true)

        locked = false
        model.reopenIfClosed()
        #expect(model.core != nil)
        #expect(model.lines.isEmpty)
        #expect(handed.last! != nil)

        handed = []
        model.reopenIfClosed()
        #expect(handed.isEmpty)
    }
}
