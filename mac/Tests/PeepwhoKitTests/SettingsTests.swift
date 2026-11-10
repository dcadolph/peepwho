import Combine
import Darwin
import Foundation
import PeepwhoCore
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

// RefusingSecrets holds nothing and refuses every write, like a Keychain that says no.
final class RefusingSecrets: SecretStore {
    // get finds nothing.
    func get(_ name: String) -> String? { nil }

    // set refuses.
    func set(_ name: String, _ value: String) throws { throw KeychainError(status: errSecAuthFailed) }

    // delete does nothing.
    func delete(_ name: String) {}
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

// CoreMemorySecrets hands the core what a MemorySecrets holds.
final class CoreMemorySecrets: NSObject, CoreSecretsProtocol {
    // store holds the secrets.
    private let store: MemorySecrets

    // init reads from store.
    init(_ store: MemorySecrets) {
        self.store = store
    }

    // get returns the value stored under name, or empty when there is none.
    func get(_ name: String?) -> String { name.flatMap(store.get) ?? "" }
}

// SilentServer listens on a local port and never answers, so a request to it waits until the
// client gives up.
final class SilentServer {
    // socket is the listening socket.
    private let socket: Int32
    // url is where it listens.
    let url: String

    // init starts listening on a free local port.
    init() throws {
        let fd = Darwin.socket(AF_INET, SOCK_STREAM, 0)
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        var size = socklen_t(MemoryLayout<sockaddr_in>.size)
        let bound = withUnsafeMutablePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, size) == 0 && listen(fd, 8) == 0 && getsockname(fd, $0, &size) == 0
            }
        }
        guard fd >= 0, bound else { throw POSIXError(.EADDRNOTAVAIL) }
        socket = fd
        url = "http://127.0.0.1:\(UInt16(bigEndian: addr.sin_port))"
    }

    // close stops listening.
    func close() {
        _ = Darwin.close(socket)
    }
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

    // Test 7: Sources group in name order, a source is as old as its oldest scope, a failed last
    // try shows beside the last success, and a run with no time says so.
    @Test func sourceAges() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        let stamp = ISO8601DateFormatter()
        func at(_ secondsAgo: TimeInterval) -> String { stamp.string(from: now.addingTimeInterval(-secondsAgo)) }
        func scope(_ source: String, _ name: String, age: Int64?, tried: TimeInterval? = nil, error: String? = nil) -> IndexScope {
            IndexScope(source: source, scope: name, ranAt: age.map { at(TimeInterval($0)) }, ageSeconds: age ?? 0,
                       attemptedAt: tried.map(at), error: error, found: nil)
        }
        let got = SourceAge.rows([
            scope("slack", "", age: 120),
            scope("git", "repo:/a", age: 3600),
            scope("git", "repo:/b", age: 2 * 86400, tried: 6 * 3600, error: "repository not found"),
            scope("jira", "project:A", age: nil),
            scope("jira", "project:B", age: 60),
            scope("github", "org:a", age: nil, tried: 300, error: "bad token"),
            scope("pagerduty", "", age: 0, tried: nil, error: "boom"),
            scope("codeowners", "file:/a", age: 60, tried: 7200, error: "older"),
            scope("codeowners", "file:/b", age: 60, tried: 60, error: "newer"),
        ], now: now)
        #expect(got == [
            SourceAge(source: "codeowners", detail: "Indexed 1 minute ago. Last try failed 1 minute ago: newer."),
            SourceAge(source: "git", detail: "Indexed 2 days ago. Last try failed 6 hours ago: repository not found."),
            SourceAge(source: "github", detail: "Indexed, time unknown. Last try failed 5 minutes ago: bad token."),
            SourceAge(source: "jira", detail: "Indexed, time unknown."),
            SourceAge(source: "pagerduty", detail: "Indexed just now. Last try failed: boom."),
            SourceAge(source: "slack", detail: "Indexed 2 minutes ago."),
        ])
        #expect(SourceAge.rows([], now: now).isEmpty)
    }

    // Test 18: A source whose runs recorded what they found says so, summed across its scopes at
    // its oldest time, and one with a run recorded before that says only when it was indexed.
    @Test func sourceAgesFound() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        let stamp = ISO8601DateFormatter()
        func scope(_ source: String, _ name: String, age: Int64, found: Found?, error: String? = nil) -> IndexScope {
            let at = stamp.string(from: now.addingTimeInterval(-TimeInterval(age)))
            return IndexScope(source: source, scope: name, ranAt: at, ageSeconds: age, attemptedAt: at, error: error,
                              found: found)
        }
        let got = SourceAge.rows([
            scope("git", "repo:/src", age: 120, found: Found(repos: 58, people: 1, channels: 0)),
            scope("slack", "", age: 120, found: Found(repos: 0, people: 5, channels: 1)),
            scope("github", "org:a", age: 3600, found: Found(repos: 0, people: 2, channels: 0)),
            scope("github", "org:b", age: 60, found: Found(repos: 0, people: 1, channels: 0)),
            scope("jira", "project:A", age: 120, found: nil),
            scope("pagerduty", "", age: 30, found: Found(repos: 0, people: 3, channels: 0)),
            scope("org-csv", "file:/a", age: 120, found: Found(repos: 0, people: 4, channels: 0)),
            scope("org-csv", "file:/b", age: 120, found: nil),
            scope("codeowners", "file:/c", age: 10800, found: Found(repos: 0, people: 3, channels: 0), error: "gone"),
        ], now: now)
        #expect(got == [
            SourceAge(source: "codeowners", detail: "Found 3 people. 3 hours ago. Last try failed 3 hours ago: gone."),
            SourceAge(source: "git", detail: "Read 58 repositories and found 1 person. 2 minutes ago."),
            SourceAge(source: "github", detail: "Found 1 person. 1 hour ago."),
            SourceAge(source: "jira", detail: "Indexed 2 minutes ago."),
            SourceAge(source: "org-csv", detail: "Indexed 2 minutes ago."),
            SourceAge(source: "pagerduty", detail: "Found 3 people. Just now."),
            SourceAge(source: "slack", detail: "Found 5 people in 1 channel. 2 minutes ago."),
        ])
        #expect(got.map(\.label) == ["CODEOWNERS", "Git", "GitHub", "Jira", "Org chart CSV", "PagerDuty", "Slack"])
        let same = SourceAge.rows([
            scope("github", "org:a", age: 3600, found: Found(repos: 0, people: 7, channels: 0)),
            scope("github", "org:b", age: 60, found: Found(repos: 0, people: 7, channels: 0)),
        ], now: now)
        #expect(same == [SourceAge(source: "github", detail: "Found 7 people. 1 hour ago.")])
        let sample = SourceAge.rows([scope("slack", "sample", age: 0, found: Found(repos: 0, people: 5, channels: 1))],
                                    sample: true, now: now)
        #expect(sample == [SourceAge(source: "slack", detail: "Loaded just now.")])
    }

    // Test 19: The summary is one sentence with singular and plural forms, zero counts left out.
    @Test func summaryWords() throws {
        func status(people: Int, teams: Int, channels: Int, sealed: Bool = true,
                    sample: Bool = false) throws -> IndexStatus {
            let json = """
            {"people":\(people),"teams":\(teams),"channels":\(channels),"subjects":0,"words":0,"conversations":0,
             "sources":{},"embeddings":false,"sealed":\(sealed),"built_at":"2026-11-01T12:00:00Z","age_seconds":10,
             "scopes":[],"sample":\(sample)}
            """
            return try Core.decode(json)
        }
        #expect(SettingsModel.summary(try status(people: 1, teams: 0, channels: 1), canLoadSample: false)
            == "1 person and 1 channel. Indexed just now. Encrypted on disk.")
        #expect(SettingsModel.summary(try status(people: 6, teams: 2, channels: 1), canLoadSample: false)
            == "6 people, 2 teams, and 1 channel. Indexed just now. Encrypted on disk.")
        #expect(SettingsModel.summary(try status(people: 1, teams: 0, channels: 0, sealed: false), canLoadSample: false)
            == "1 person. Indexed just now.")
        #expect(SettingsModel.summary(try status(people: 223, teams: 9, channels: 4, sample: true), canLoadSample: true)
            == "Sample company, not real people. 223 people, 9 teams, and 4 channels. Loaded just now. "
            + "Encrypted on disk.")
        #expect(SettingsModel.summary(nil, canLoadSample: false) == "The index is not open.")
    }

    // Test 21: The People sheet lists everyone the real core holds after an index, and the
    // headline's count matches it.
    @Test func loadPeople() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await model.loadStatus()
        #expect(model.peopleWords == nil)
        await model.pick(repo, as: .git)
        await model.loadPeople()
        #expect(model.peopleError == nil)
        #expect(model.people.map(\.name) == ["Jane Doe"])
        #expect(model.peopleWords == "1 person")
        #expect(model.summary.hasPrefix("1 person."))
    }

    // Test 20: The caption under the buttons names the refresh interval as it is set.
    @Test func refreshNote() throws {
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: tempDir("peepwho-data"), open: { throw CocoaError(.fileNoSuchFile) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        #expect(model.refreshNote == "Index Now reads every source from the start. Refresh Now picks up what "
            + "changed since, and runs on its own every 6 hours.")
        defaults.set(3600.0, forKey: SettingsModel.intervalKey)
        #expect(model.refreshNote.hasSuffix("every 1 hour."))
        defaults.set(90.0 * 60, forKey: SettingsModel.intervalKey)
        #expect(model.refreshNote.hasSuffix("every 90 minutes."))
    }

    // Test 8: Through the real core, a repo that goes missing keeps its last success and shows the
    // failed refresh beside it.
    @Test func sourceAgesThroughCore() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await model.pick(repo, as: .git)
        let found = "Read 1 repository and found 1 person. Just now."
        #expect(model.sourceAges == [SourceAge(source: "git", detail: found)])
        #expect(model.sourceAges.first?.label == "Git")
        try FileManager.default.removeItem(at: repo)
        await model.refresh()
        let row = try #require(model.sourceAges.first)
        #expect(row.source == "git")
        #expect(row.detail.hasPrefix("\(found) Last try failed just now: "))
    }

    // Test 9: A status the core refuses says why, rather than leaving the window claiming the index
    // is closed.
    @Test func statusFailureShows() async throws {
        let dir = tempDir("peepwho-data")
        defer { try? FileManager.default.removeItem(at: dir) }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try "{".write(to: dir.appendingPathComponent("refresh.json"), atomically: true, encoding: .utf8)
        await model.loadStatus()
        #expect(model.status == nil)
        #expect(model.lines.first?.contains("refresh") == true)
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
        #expect(Scopes.load(secrets).jiraProjects == "KAFKA")
    }

    // Test 10: Scopes live in the secret store and never in preferences, and a stored value that
    // does not decode reads as empty scopes.
    @Test func scopesStayOutOfPreferences() throws {
        let planted = "PLANTED-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let secrets = MemorySecrets()
        let model = SettingsModel(dir: tempDir("peepwho-data"), open: { try Core.open(dir: tempDir("x"), key: "", secrets: KeychainSecrets()) },
                                  secrets: secrets, places: MemoryPlaces(), defaults: defaults)
        model.scopes.jiraProjects = planted
        model.scopes.jiraServer = true
        #expect(secrets.values[SecretName.scopes]?.contains(planted) == true)
        for store in [defaults, UserDefaults.standard] {
            #expect(!String(describing: store.dictionaryRepresentation()).contains(planted))
        }
        let back = Scopes.load(secrets)
        #expect(back == model.scopes)
        secrets.values[SecretName.scopes] = "{"
        #expect(Scopes.load(secrets) == Scopes())
    }

    // Test 11: A failed scope save says so instead of dropping it in silence.
    @Test func scopeSaveFailureShows() throws {
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let secrets = RefusingSecrets()
        let model = SettingsModel(dir: tempDir("peepwho-data"), open: { try Core.open(dir: tempDir("x"), key: "", secrets: KeychainSecrets()) },
                                  secrets: secrets, places: MemoryPlaces(), defaults: defaults)
        model.scopes.jiraProjects = "KAFKA"
        #expect(model.lines.first?.hasPrefix("Could not save to the Keychain") == true)
    }

    // Test 12: A bookmark lives in the secret store and never in preferences, resolves back to
    // its place, and forgetting it leaves nothing.
    @Test func bookmarksStayOutOfPreferences() throws {
        let place = tempDir("peepwho-place-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: place, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: place) }
        let secrets = MemorySecrets()
        try Bookmarks.save(place, in: .git, store: secrets)
        #expect(Bookmarks.has(.git, store: secrets))
        #expect(secrets.values[BookmarkSlot.git.storeKey] != nil)
        #expect(UserDefaults.standard.object(forKey: BookmarkSlot.git.storeKey) == nil)
        let resolved = try #require(try Bookmarks.resolve(.git, store: secrets))
        defer { resolved.url.stopAccessingSecurityScopedResource() }
        #expect(resolved.url.resolvingSymlinksInPath().path == place.resolvingSymlinksInPath().path)
        Bookmarks.forget(.git, store: secrets)
        #expect(!Bookmarks.has(.git, store: secrets))
        #expect(try Bookmarks.resolve(.git, store: secrets) == nil)
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
        let clock = Date(timeIntervalSince1970: 1_800_000_000)
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults, clock: { clock })
        var handed: [Core?] = []
        model.onCore = { handed.append($0) }
        let indexed = "Indexed at \(clock.formatted(date: .omitted, time: .shortened))."

        await model.pick(repo, as: .git)
        #expect(model.lines == [indexed, readOneRepo(model)])
        #expect(model.lastRun == LastRun(kind: .indexed, at: clock))
        #expect(model.placeNames[.git] == repo.lastPathComponent)
        #expect((model.status?.people ?? 0) > 0)
        #expect(model.status?.sources["git"] != nil)

        await model.refresh()
        let refreshed = "Refreshed at \(clock.formatted(date: .omitted, time: .shortened))."
        #expect(model.lines == [refreshed, refreshedOneRepo(model)])
        #expect(model.lastRun?.kind == .refreshed)

        await model.indexAll()
        #expect(model.lines == [indexed, readOneRepo(model)])
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
        // A task on the main actor runs to its first wait before the yield returns, so the pick is
        // busy here however busy the machine is.
        let picked = Task { await model.pick(repo, as: .git) }
        await Task.yield()
        #expect(model.busy != nil)
        await model.wipe()
        #expect(model.lines.first?.hasSuffix("is still running. Try again when it finishes.") == true)
        await model.indexAll()
        #expect(model.lines.first?.hasSuffix("is still running. Try again when it finishes.") == true)
        await picked.value
        #expect(model.busy == nil)
        #expect(model.status?.sources["git"] != nil)
        #expect(FileManager.default.fileExists(atPath: dir.path))
        #expect(places.open == 0)
    }

    // Test 7: Progress lines show during a run with no path into the data directory.
    @Test func progressShowsNoDataPath() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        var seen: [String] = []
        let watching = model.$progress.sink { line in
            if let line { seen.append(line) }
        }
        defer { watching.cancel() }
        await model.pick(repo, as: .git)
        #expect(!seen.isEmpty)
        #expect(!seen.contains { $0.contains(dir.path) })
        #expect(seen.contains { $0.contains("index.json") })
        #expect(model.progress == nil)
    }

    // Test 18: A pick whose first index fails is forgotten, so no row claims a place that never
    // indexed.
    @Test func failedPickIsForgotten() async throws {
        let place = tempDir("peepwho-place")
        let dir = tempDir("peepwho-data")
        try FileManager.default.createDirectory(at: place, withIntermediateDirectories: true)
        defer {
            try? FileManager.default.removeItem(at: place)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let places = MemoryPlaces()
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        await model.pick(place, as: .git)
        #expect(model.lines.first?.contains("folder with no repository in it") == true)
        #expect(places.urls[.git] == nil)
        #expect(model.placeNames[.git] == nil)
        #expect(places.open == 0)
    }

    // Test 8: A cancel between jobs leaves the next job unrun and clears the run's state, and
    // every place is released.
    @Test func cancelStopsTheJobLoop() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        let csv = tempDir("peepwho-roster").appendingPathExtension("csv")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
            try? FileManager.default.removeItem(at: csv)
        }
        try "name,email,title,team,topics\nAnn Lee,ann@x.com,Engineer,Billing,ledger\n"
            .write(to: csv, atomically: true, encoding: .utf8)
        let places = MemoryPlaces()
        places.urls = [.git: repo, .csv: csv]
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        async let all: Void = model.indexAll()
        while model.busy == nil { await Task.yield() }
        #expect(model.canCancel)
        model.cancel()
        #expect(model.progress == "Canceling")
        await all
        #expect(model.lines.last == "Canceled.")
        #expect(!model.lines.contains { $0.hasPrefix("org-csv") })
        #expect(model.status?.sources["org-csv"] == nil)
        #expect(model.busy == nil)
        #expect(model.progress == nil)
        #expect(!model.canCancel)
        #expect(places.open == 0)
    }

    // Test 9: Cancel reaches a run stuck on a server that never answers, even when pressed the
    // moment the run starts, and the run saves nothing.
    @Test func cancelReachesAStuckServer() async throws {
        let server = try SilentServer()
        defer { server.close() }
        let dir = tempDir("peepwho-data")
        defer { try? FileManager.default.removeItem(at: dir) }
        let secrets = MemorySecrets()
        secrets.values = [SecretName.jiraURL: server.url, SecretName.jiraToken: "token"]
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: CoreMemorySecrets(secrets)) },
                                  secrets: secrets, places: MemoryPlaces(), defaults: defaults)
        var scopes = model.scopes
        scopes.jiraProjects = "KAFKA"
        scopes.jiraServer = true
        model.scopes = scopes
        let clock = ContinuousClock()
        let started = clock.now
        async let all: Void = model.indexAll()
        while model.busy == nil { await Task.yield() }
        model.cancel()
        await all
        // A lost cancel waits out the 120 second client timeout. A busy machine can take 18 seconds.
        #expect(started.duration(to: clock.now) < .seconds(60))
        #expect(model.lines == ["Canceled."])
        #expect(model.status?.builtAt == nil)
        #expect(model.busy == nil)
    }

    // Test 10: Cancel does nothing with no run going, and nothing it did reaches the next run.
    @Test func cancelWithNoRun() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        model.cancel()
        #expect(model.progress == nil)
        await model.pick(repo, as: .git)
        #expect(model.lines == [try #require(model.lastRun).line, readOneRepo(model)])
    }

    // Test 11: Picking a new place into a slot stops refreshing the old one, and a place that went
    // away shows in the menu after an automatic refresh, until picking another clears it.
    @Test func replacedPlaceStopsRefreshing() async throws {
        let (first, second) = (try gitRepo(), try gitRepo())
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: first)
            try? FileManager.default.removeItem(at: second)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        defaults.set(60.0, forKey: SettingsModel.intervalKey)
        let places = MemoryPlaces()
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        var heard: [String?] = []
        model.onRefreshFailure = { heard.append($0) }

        await model.pick(first, as: .git)
        #expect(model.refreshFailure == nil)
        try FileManager.default.removeItem(at: first)
        #expect(await model.refreshIfStale(now: Date().addingTimeInterval(3600)))
        let failure = "Last refresh failed for git. Open Settings to see why."
        #expect(model.refreshFailure == failure)
        #expect(heard.last == failure)
        #expect(!(model.refreshFailure ?? "").contains(first.path))

        await model.pick(second, as: .git)
        #expect(model.status?.scopes.map(\.scope) == ["repo:\(second.path)"])
        #expect(model.refreshFailure == nil)
        #expect(heard.last == .some(nil))
        await model.refresh()
        #expect(model.lines == [try #require(model.lastRun).line, refreshedOneRepo(model)])
    }

    // Test 12: Forget stops refreshing the place, leaves the index, refuses while a run is going,
    // and refresh then says there is nothing to do.
    @Test func forgetStopsRefreshing() async throws {
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
        await model.pick(repo, as: .git)
        #expect(model.status?.scopes.count == 1)

        async let running: Void = model.indexAll()
        while model.busy == nil { await Task.yield() }
        await model.forgetPlace(.git)
        #expect(model.lines.first?.hasSuffix("is still running. Try again when it finishes.") == true)
        #expect(places.has(.git))
        await running

        await model.forgetPlace(.git)
        #expect(model.placeNames[.git] == nil)
        #expect(!places.has(.git))
        #expect(model.status?.scopes.isEmpty == true)
        #expect((model.status?.people ?? 0) > 0)
        await model.refresh()
        #expect(model.lines == ["Nothing to refresh: index a source at least once first."])
    }

    // Test 17: A new pick that fails to index still stops refreshing the place it replaced, since
    // the slot no longer holds access to it.
    @Test func failedPickDropsOldScope() async throws {
        let first = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: first)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await model.pick(first, as: .git)
        #expect(model.status?.scopes.count == 1)
        await model.pick(tempDir("peepwho-missing"), as: .git)
        #expect(!model.lines.contains { $0.hasPrefix("Git: read") })
        #expect(!model.lines.contains { $0.hasPrefix("Indexed at") })
        #expect(model.status?.scopes.isEmpty == true)
    }

    // Test 15: Indexing again keeps only what this run read, so a scope changed in Settings stops
    // being read on refresh.
    @Test func indexReplacesOldScope() async throws {
        let (first, second) = (try gitRepo(), try gitRepo())
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: first)
            try? FileManager.default.removeItem(at: second)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let places = MemoryPlaces()
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        await model.pick(first, as: .git)
        places.urls[.git] = second
        await model.indexAll()
        #expect(model.status?.scopes.map(\.scope) == ["repo:\(second.path)"])
    }

    // Test 16: The menu names sources that failed, once each and in order, leaves out a source
    // missing a secret it needs, and says nothing when none failed.
    @Test func refreshFailureNames() {
        func scope(_ source: String, _ name: String, error: String? = nil) -> IndexScope {
            IndexScope(source: source, scope: name, ranAt: nil, ageSeconds: 0, attemptedAt: nil, error: error,
                       found: nil)
        }
        let jira = [SecretName.jiraURL, SecretName.jiraToken]
        #expect(SettingsModel.refreshFailure([], saved: []) == nil)
        #expect(SettingsModel.refreshFailure([scope("git", "repo:/a")], saved: []) == nil)
        #expect(SettingsModel.refreshFailure([scope("git", "repo:/a", error: "lost")], saved: [])
            == "Last refresh failed for git. Open Settings to see why.")
        let two = [scope("jira", "project:A", error: "401"), scope("jira", "project:B", error: "401"),
                   scope("git", "repo:/a", error: "lost")]
        #expect(SettingsModel.refreshFailure(two, saved: Set(jira))
            == "Last refresh failed for git, jira. Open Settings to see why.")
        #expect(SettingsModel.refreshFailure([scope("jira", "project:A", error: "no token")],
                                             saved: [SecretName.jiraURL]) == nil)
        for source in ["slack", "github", "jira", "confluence", "pagerduty"] {
            #expect(!SecretName.required(by: source).isEmpty)
        }
    }

    // Test 13: A place stays when the index is not open to forget it in.
    @Test func forgetWithNoCoreKeepsPlace() async throws {
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let places = MemoryPlaces()
        places.urls = [.git: URL(fileURLWithPath: "/tmp/repo")]
        let model = SettingsModel(dir: tempDir("peepwho-data"), open: { throw KeychainError(status: errSecInteractionNotAllowed) },
                                  secrets: MemorySecrets(), places: places, defaults: defaults)
        await model.forgetPlace(.git)
        #expect(model.lines == ["The index is not open."])
        #expect(places.has(.git))
    }

    // Test 14: A refresh that ran no scope says so instead of showing nothing.
    @Test func refreshOfNothingSaysSo() async throws {
        let repo = try gitRepo()
        let dir = tempDir("peepwho-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "peepwho-test-\(UUID().uuidString)"))
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await model.pick(repo, as: .git)
        try Data(#"{"jira":{}}"#.utf8).write(to: dir.appendingPathComponent("refresh.json"))
        await model.refresh()
        #expect(model.lines == ["Nothing to refresh: index a source at least once first."])
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

// readOneRepo is the finish line a pick of one test repository shows, with the people model's
// status counts.
@MainActor
func readOneRepo(_ model: SettingsModel) -> String {
    let people = model.status?.people ?? 0
    return "Git: read 1 repository. The index holds \(Count.words(people, "person", "people"))."
}

// refreshedOneRepo is the line a refresh of one picked repo ends with, since the source holds
// everyone the index holds.
@MainActor
func refreshedOneRepo(_ model: SettingsModel) -> String {
    let people = model.status?.people ?? 0
    return "Git: read 1 repository again and found \(Count.words(people, "person", "people"))."
}
