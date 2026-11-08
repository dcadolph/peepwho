import Foundation

// Scopes holds what each token source reads, which the user types once and refresh replays.
public struct Scopes: Equatable, Sendable {
    // githubRepos are owner/name repositories, comma separated.
    public var githubRepos = ""
    // githubOrg is an org to read every repository of, used when githubRepos is empty.
    public var githubOrg = ""
    // jiraProjects are project keys, comma separated.
    public var jiraProjects = ""
    // jiraServer selects Jira Server or Data Center over Cloud.
    public var jiraServer = false
    // confluenceSpaces are space keys, comma separated.
    public var confluenceSpaces = ""
    // confluenceServer selects Confluence Server or Data Center over Cloud.
    public var confluenceServer = false

    // init makes empty scopes.
    public init() {}

    // load reads the scopes saved in defaults.
    public static func load(_ defaults: UserDefaults) -> Scopes {
        var s = Scopes()
        s.githubRepos = defaults.string(forKey: "scope.githubRepos") ?? ""
        s.githubOrg = defaults.string(forKey: "scope.githubOrg") ?? ""
        s.jiraProjects = defaults.string(forKey: "scope.jiraProjects") ?? ""
        s.jiraServer = defaults.bool(forKey: "scope.jiraServer")
        s.confluenceSpaces = defaults.string(forKey: "scope.confluenceSpaces") ?? ""
        s.confluenceServer = defaults.bool(forKey: "scope.confluenceServer")
        return s
    }

    // save writes the scopes to defaults.
    public func save(_ defaults: UserDefaults) {
        defaults.set(githubRepos, forKey: "scope.githubRepos")
        defaults.set(githubOrg, forKey: "scope.githubOrg")
        defaults.set(jiraProjects, forKey: "scope.jiraProjects")
        defaults.set(jiraServer, forKey: "scope.jiraServer")
        defaults.set(confluenceSpaces, forKey: "scope.confluenceSpaces")
        defaults.set(confluenceServer, forKey: "scope.confluenceServer")
    }
}

// IndexJob is one core Index call: a source and the command line flags it runs with.
public struct IndexJob: Equatable, Sendable {
    // source is the source name the core reads.
    public let source: String
    // flags are the index flags, one argument per element.
    public let flags: [String]

    // forPlace is the job that reads a picked place.
    public static func forPlace(_ slot: BookmarkSlot, _ url: URL) -> IndexJob {
        switch slot {
        case .git:
            return IndexJob(source: "git", flags: ["--repo-path", url.path])
        case .csv:
            return IndexJob(source: "org-csv", flags: ["--file", url.path])
        case .codeowners:
            return IndexJob(source: "codeowners", flags: ["--file", url.path])
        }
    }

    // forTokens lists the jobs for every token source whose secrets are stored and whose scope
    // is filled in. has reports whether a secret is stored under a name.
    public static func forTokens(_ scopes: Scopes, has: (String) -> Bool) -> [IndexJob] {
        var jobs: [IndexJob] = []
        if has(SecretName.slackToken) {
            jobs.append(IndexJob(source: "slack", flags: []))
        }
        if has(SecretName.githubToken) {
            let repos = list(scopes.githubRepos)
            let org = scopes.githubOrg.trimmingCharacters(in: .whitespaces)
            if !repos.isEmpty {
                jobs.append(IndexJob(source: "github", flags: repos.flatMap { ["--repo", $0] }))
            } else if !org.isEmpty {
                jobs.append(IndexJob(source: "github", flags: ["--github-org", org]))
            }
        }
        let projects = list(scopes.jiraProjects)
        if has(SecretName.jiraURL), has(SecretName.jiraToken), !projects.isEmpty {
            let server = scopes.jiraServer ? ["--jira-server"] : []
            jobs.append(IndexJob(source: "jira", flags: server + projects.flatMap { ["--jira-project", $0] }))
        }
        let spaces = list(scopes.confluenceSpaces)
        if has(SecretName.confluenceToken), !spaces.isEmpty {
            let server = scopes.confluenceServer ? ["--confluence-server"] : []
            jobs.append(IndexJob(source: "confluence", flags: server + spaces.flatMap { ["--confluence-space", $0] }))
        }
        if has(SecretName.pagerdutyToken) {
            jobs.append(IndexJob(source: "pagerduty", flags: []))
        }
        return jobs
    }

    // list splits a comma separated field into its trimmed, non-empty parts.
    static func list(_ field: String) -> [String] {
        field.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }
}

// Age says how long ago something happened in plain words.
public enum Age {
    // words turns seconds into "just now", "5 minutes ago", "1 hour ago", or "3 days ago".
    public static func words(_ seconds: Int64) -> String {
        switch seconds {
        case ..<60:
            return "just now"
        case ..<3600:
            return count(seconds / 60, "minute")
        case ..<86400:
            return count(seconds / 3600, "hour")
        default:
            return count(seconds / 86400, "day")
        }
    }

    // count is n of unit, made plural past one, followed by ago.
    static func count(_ n: Int64, _ unit: String) -> String {
        "\(n) \(unit)\(n == 1 ? "" : "s") ago"
    }
}

// SettingsModel drives the settings window: tokens, picked places, what the index holds, and
// the Index, Refresh, and Wipe actions. It owns the open core and hands it to the search.
@MainActor
public final class SettingsModel: ObservableObject {
    // status is what the index held when last read, nil before the first read.
    @Published public private(set) var status: IndexStatus?
    // busy names the action running, nil when idle.
    @Published public private(set) var busy: String?
    // lines are the outcomes of the last action, one sentence each.
    @Published public private(set) var lines: [String] = []
    // saved are the secret names the store holds.
    @Published public private(set) var saved: Set<String> = []
    // placeNames are the names of the picked places, by slot.
    @Published public private(set) var placeNames: [BookmarkSlot: String] = [:]
    // scopes are what the token sources read, saved on every change.
    @Published public var scopes: Scopes {
        didSet { scopes.save(defaults) }
    }
    // core is the open data directory, nil when it did not open.
    public private(set) var core: Core?
    // lastAttempt is when refreshIfStale last started a refresh, so a refresh that keeps failing
    // waits a full interval before it runs again.
    private var lastAttempt: Date?
    // onCore hears every core the model opens, so the search follows a wipe.
    public var onCore: ((Core?) -> Void)?

    // dir is the data directory Wipe removes.
    private let dir: URL
    // open opens dir under its key.
    private let open: () throws -> Core
    // secrets stores tokens and site addresses.
    private let secrets: SecretStore
    // places stores the picked folders and files.
    private let places: PlaceStore
    // defaults stores the scopes.
    private let defaults: UserDefaults

    // init opens dir with open and reads which secrets and places are saved.
    public init(dir: URL, open: @escaping () throws -> Core, secrets: SecretStore, places: PlaceStore,
                defaults: UserDefaults = .standard) {
        self.dir = dir
        self.open = open
        self.secrets = secrets
        self.places = places
        self.defaults = defaults
        scopes = Scopes.load(defaults)
        reopen()
        readSaved()
    }

    // saveSecret stores value under name, or deletes the secret when value is empty.
    public func saveSecret(_ name: String, _ value: String) {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            forgetSecret(name)
            return
        }
        do {
            try secrets.set(name, trimmed)
        } catch {
            lines = ["Could not save to the Keychain: \(error)."]
        }
        readSaved()
    }

    // forgetSecret deletes the secret stored under name.
    public func forgetSecret(_ name: String) {
        secrets.delete(name)
        readSaved()
    }

    // pick remembers url for slot and indexes it.
    public func pick(_ url: URL, as slot: BookmarkSlot) async {
        do {
            try places.save(url, in: slot)
        } catch {
            lines = ["Could not keep access to \(url.lastPathComponent): \(error)."]
            return
        }
        readSaved()
        await run("Indexing \(url.lastPathComponent)") { core in
            guard let url = try self.places.resolve(slot) else { return [] }
            defer { self.places.release(url) }
            return [try await self.index(core, IndexJob.forPlace(slot, url))]
        }
    }

    // forgetPlace drops slot's place. What it already put in the index stays until a wipe.
    public func forgetPlace(_ slot: BookmarkSlot) {
        places.forget(slot)
        readSaved()
    }

    // indexAll indexes every picked place and every token source with a scope.
    public func indexAll() async {
        await run("Indexing") { core in
            let opened = try self.resolveAll()
            defer { opened.values.forEach(self.places.release) }
            var jobs = BookmarkSlot.allCases.compactMap { slot in opened[slot].map { IndexJob.forPlace(slot, $0) } }
            jobs += IndexJob.forTokens(self.scopes) { self.secrets.get($0) != nil }
            guard !jobs.isEmpty else { return ["Nothing to index yet. Pick a folder or add a token first."] }
            var out: [String] = []
            for job in jobs {
                do {
                    out.append(try await self.index(core, job))
                } catch {
                    out.append("\(job.source): \(SearchModel.describe(error))")
                }
            }
            return out
        }
    }

    // refresh replays every scope indexed before, with access to every picked place held for the
    // call, since a recorded git or file scope reads that place again.
    public func refresh() async {
        await run("Refreshing") { core in
            if try await core.status().builtAt == nil {
                return ["Nothing to refresh yet. Index something first."]
            }
            let opened = try self.resolveAll()
            defer { opened.values.forEach(self.places.release) }
            let result = try await core.refresh()
            return result.results.map { r in
                r.ok ? "\(r.source): refreshed \(r.scope)." : "\(r.source): \(r.error ?? "failed")."
            }
        }
    }

    // intervalKey is the UserDefaults key for how many seconds old the index may get before
    // refreshIfStale refreshes it.
    public static let intervalKey = "PeepwhoRefreshIntervalSeconds"
    // defaultInterval is six hours.
    public static let defaultInterval: TimeInterval = 6 * 3600

    // refreshInterval is the stored interval, or six hours when none is set.
    public var refreshInterval: TimeInterval {
        let stored = defaults.double(forKey: Self.intervalKey)
        return stored > 0 ? stored : Self.defaultInterval
    }

    // reopenIfClosed tries the data directory again after an open that failed, such as under a
    // locked Keychain. The menu opening and the Mac waking call it, and the timer does not, so a
    // Keychain prompt never repeats on its own.
    public func reopenIfClosed() {
        guard core == nil, busy == nil else { return }
        reopen()
        if core != nil {
            lines = []
        }
    }

    // refreshIfStale refreshes when the index at now is older than the interval and no refresh
    // started within the interval, and reports whether it refreshed. The menu opening, the Mac
    // waking, and the timer all call it.
    @discardableResult
    public func refreshIfStale(now: Date = Date()) async -> Bool {
        guard busy == nil, let core else { return false }
        let interval = refreshInterval
        if let lastAttempt, now.timeIntervalSince(lastAttempt) < interval {
            return false
        }
        guard let status = try? await core.status(now: now), status.builtAt != nil,
              TimeInterval(status.ageSeconds) >= interval
        else { return false }
        lastAttempt = now
        await refresh()
        return true
    }

    // wipe deletes the data directory and opens a fresh, empty one in its place. Tokens and
    // picked places stay until forgotten one by one.
    public func wipe() async {
        busy = "Wiping"
        core = nil
        onCore?(nil)
        do {
            let removed = try Core.wipe(dir: dir).removed ?? []
            lines = ["Deleted \(removed.count) files. The index is empty."]
        } catch {
            lines = ["Wipe failed: \(SearchModel.describe(error))"]
        }
        reopen()
        busy = nil
        await loadStatus()
    }

    // loadStatus reads what the index holds at now.
    public func loadStatus(now: Date = Date()) async {
        guard let core else {
            status = nil
            return
        }
        status = try? await core.status(now: now)
    }

    // has reports whether a secret is stored under name.
    public func has(_ name: String) -> Bool {
        saved.contains(name)
    }

    // run marks the model busy with label, runs body on the open core, shows its lines, and
    // reads the status again.
    private func run(_ label: String, _ body: (Core) async throws -> [String]) async {
        guard let core else {
            lines = ["The index is not open."]
            return
        }
        busy = label
        do {
            lines = try await body(core)
        } catch {
            lines = [SearchModel.describe(error)]
        }
        busy = nil
        await loadStatus()
    }

    // index runs job and says what it read.
    private func index(_ core: Core, _ job: IndexJob) async throws -> String {
        let result = try await core.index(job.source, flags: job.flags)
        return "\(result.source): indexed \(result.scope)."
    }

    // resolveAll starts access to every picked place, keyed by slot. The caller releases them.
    private func resolveAll() throws -> [BookmarkSlot: URL] {
        var opened: [BookmarkSlot: URL] = [:]
        do {
            for slot in BookmarkSlot.allCases where places.has(slot) {
                opened[slot] = try places.resolve(slot)
            }
        } catch {
            opened.values.forEach(places.release)
            throw error
        }
        return opened
    }

    // reopen opens the data directory and hands the result to the search.
    private func reopen() {
        do {
            core = try open()
        } catch {
            core = nil
            lines = ["The index did not open: \(SearchModel.describe(error))"]
        }
        onCore?(core)
    }

    // readSaved reads which secrets and places the stores hold.
    private func readSaved() {
        let names = [
            SecretName.slackToken, SecretName.githubToken, SecretName.jiraURL, SecretName.jiraEmail,
            SecretName.jiraToken, SecretName.confluenceURL, SecretName.confluenceEmail,
            SecretName.confluenceToken, SecretName.pagerdutyToken,
        ]
        saved = Set(names.filter { secrets.get($0) != nil })
        var named: [BookmarkSlot: String] = [:]
        for slot in BookmarkSlot.allCases where places.has(slot) {
            if let url = try? places.resolve(slot) {
                named[slot] = url.lastPathComponent
                places.release(url)
            } else {
                named[slot] = "Needs picking again"
            }
        }
        placeNames = named
    }
}
