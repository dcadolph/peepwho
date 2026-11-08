import Foundation

// Scopes holds what each token source reads, which the user types once and refresh replays.
public struct Scopes: Equatable, Sendable, Codable {
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

    // load reads the scopes saved in secrets. Nothing saved, or nothing readable, is empty scopes.
    public static func load(_ secrets: SecretStore) -> Scopes {
        guard let text = secrets.get(SecretName.scopes),
              let scopes = try? JSONDecoder().decode(Scopes.self, from: Data(text.utf8)) else { return Scopes() }
        return scopes
    }

    // save writes the scopes to secrets. They name the repositories, projects, and spaces a person
    // reads, so they stay out of the plain preferences file beside the sealed index.
    public func save(_ secrets: SecretStore) throws {
        try secrets.set(SecretName.scopes, String(decoding: try JSONEncoder().encode(self), as: UTF8.self))
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
        if has(SecretName.confluenceURL), has(SecretName.confluenceToken), !spaces.isEmpty {
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

// SourceAge says how fresh one source is, for the settings window.
public struct SourceAge: Equatable, Sendable, Identifiable {
    // source is the source name.
    public let source: String
    // detail is when the source last worked and, if its last try failed, why.
    public let detail: String

    // id is the source name, since rows hold one source each.
    public var id: String { source }

    // rows groups scopes by source in name order. A source is as old as its oldest scope, and is
    // of unknown age when any scope is. A sample source reads as loaded, not indexed. now is the
    // clock the ages were read at.
    public static func rows(_ scopes: [IndexScope], sample: Bool = false, now: Date) -> [SourceAge] {
        let verb = sample ? "Loaded" : "Indexed"
        return Dictionary(grouping: scopes, by: \.source).sorted { $0.key < $1.key }.map { source, group in
            let known = group.allSatisfy { $0.ranAt != nil }
            let oldest = group.map(\.ageSeconds).max() ?? 0
            var detail = known ? "\(verb) \(Age.words(oldest))." : "\(verb), time unknown."
            if let failure = lastFailure(group, now: now) {
                detail += " \(failure)"
            }
            return SourceAge(source: source, detail: detail)
        }
    }

    // lastFailure says how the newest failed try in group went, nil when none failed.
    static func lastFailure(_ group: [IndexScope], now: Date) -> String? {
        let parser = ISO8601DateFormatter()
        let failed = group.compactMap { scope -> (error: String, at: Date?)? in
            scope.error.map { ($0, scope.attemptedAt.flatMap(parser.date(from:))) }
        }
        guard let newest = failed.max(by: { ($0.at ?? .distantPast) < ($1.at ?? .distantPast) }) else {
            return nil
        }
        guard let at = newest.at else { return "Last try failed: \(newest.error)." }
        return "Last try failed \(Age.words(Int64(now.timeIntervalSince(at)))): \(newest.error)."
    }
}

// SettingsModel drives the settings window: tokens, picked places, what the index holds, and
// the Index, Refresh, and Wipe actions. It owns the open core and hands it to the search.
@MainActor
public final class SettingsModel: ObservableObject {
    // status is what the index held when last read, nil before the first read.
    @Published public private(set) var status: IndexStatus?
    // statusAt is the clock status was read at.
    @Published public private(set) var statusAt = Date()
    // busy names the action running, nil when idle.
    @Published public private(set) var busy: String?
    // lines are the outcomes of the last action, one sentence each.
    @Published public private(set) var lines: [String] = []
    // saved are the secret names the store holds.
    @Published public private(set) var saved: Set<String> = []
    // placeNames are the names of the picked places, by slot.
    @Published public private(set) var placeNames: [BookmarkSlot: String] = [:]
    // progress is the newest line of the run going now, nil when there is none yet.
    @Published public private(set) var progress: String?
    // canCancel reports whether the run going now can be stopped.
    @Published public private(set) var canCancel = false
    // scopes are what the token sources read, saved on every change.
    @Published public var scopes: Scopes {
        didSet { saveScopes() }
    }
    // core is the open data directory, nil when it did not open.
    public private(set) var core: Core?
    // lastAttempt is when refreshIfStale last started a refresh, so a refresh that keeps failing
    // waits a full interval before it runs again.
    private var lastAttempt: Date?
    // onCore hears every core the model opens, so the search follows a wipe.
    public var onCore: ((Core?) -> Void)?
    // onSample hears whether the index holds the sample company after each status read.
    public var onSample: ((Bool) -> Void)?

    // dir is the data directory Wipe removes.
    private let dir: URL
    // open opens dir under its key.
    private let open: () throws -> Core
    // secrets stores tokens and site addresses.
    private let secrets: SecretStore
    // places stores the picked folders and files.
    private let places: PlaceStore
    // canceled reports that the person stopped the run going now.
    private var canceled = false
    // defaults stores the refresh interval.
    private let defaults: UserDefaults
    // sampleDir is the sample company the app bundles, nil when it has none.
    private let sampleDir: URL?

    // init opens dir with open and reads which secrets and places are saved.
    public init(dir: URL, open: @escaping () throws -> Core, secrets: SecretStore, places: PlaceStore,
                defaults: UserDefaults = .standard, sampleDir: URL? = nil) {
        self.dir = dir
        self.sampleDir = sampleDir
        self.open = open
        self.secrets = secrets
        self.places = places
        self.defaults = defaults
        scopes = Scopes.load(secrets)
        reopen()
        readSaved()
    }

    // saveScopes keeps the scopes in the secret store, or says why it could not.
    private func saveScopes() {
        do {
            try scopes.save(secrets)
        } catch {
            lines = ["Could not save to the Keychain: \(error)."]
        }
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
        guard refuseWhileBusy() else { return }
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
                guard !self.canceled else { break }
                self.busy = "Indexing \(job.source)"
                self.progress = nil
                do {
                    out.append(try await self.index(core, job))
                } catch {
                    guard !self.canceled else { break }
                    out.append("\(job.source): \(SearchModel.describe(error))")
                }
                self.lines = out
            }
            return self.canceled ? out + ["Canceled."] : out
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
            let done = result.results.map { r in
                r.ok ? "\(r.source): refreshed \(r.scope)." : "\(r.source): \(r.error ?? "failed")."
            }
            return result.canceled == true ? done + ["Canceled."] : done
        }
    }

    // canLoadSample reports whether the sample company can load now: the app bundles one and the
    // index is empty or already holds it, so real data is never in the way.
    public var canLoadSample: Bool {
        guard sampleDir != nil, let status else { return false }
        return status.builtAt == nil || status.sample
    }

    // loadSample loads the bundled sample company.
    public func loadSample() async {
        guard let sampleDir else { return }
        await run("Loading the sample company", cancelable: false) { core in
            let got = try await core.importSample(from: sampleDir)
            return ["Loaded the sample company: \(got.people) people from \(got.sources) sources. Wipe removes it."]
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
        guard let status = try? await core.status(now: now), status.builtAt != nil, !status.sample,
              TimeInterval(status.ageSeconds) >= interval, busy == nil
        else { return false }
        lastAttempt = now
        await refresh()
        return true
    }

    // wipe deletes the data directory and opens a fresh, empty one in its place. Tokens and
    // picked places stay until forgotten one by one.
    public func wipe() async {
        guard refuseWhileBusy() else { return }
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
        statusAt = now
        do {
            status = try await core.status(now: now)
        } catch {
            status = nil
            lines = [SearchModel.describe(error)]
        }
        onSample?(status?.sample == true)
    }

    // summary says how much the index holds and how old it is.
    public var summary: String {
        guard let s = status else { return "The index is not open." }
        guard s.builtAt != nil else {
            let sample = canLoadSample ? ", or load the sample company to look around." : "."
            return "Nothing indexed yet. Pick a folder or add a token, then Index Now\(sample)"
        }
        let counts = "\(s.people) people, \(s.teams) teams, \(s.channels) channels."
        let sealed = s.sealed ? " Encrypted on disk." : ""
        if s.sample {
            return "Sample company, not real people. \(counts) Loaded \(Age.words(s.ageSeconds)).\(sealed)"
        }
        return "\(counts) Indexed \(Age.words(s.ageSeconds)).\(sealed)"
    }

    // sourceAges says how fresh each recorded source is, as of the last status read.
    public var sourceAges: [SourceAge] {
        SourceAge.rows(status?.scopes ?? [], sample: status?.sample == true, now: statusAt)
    }

    // has reports whether a secret is stored under name.
    public func has(_ name: String) -> Bool {
        saved.contains(name)
    }

    // refuseWhileBusy reports whether the model is idle, and says what to wait for when it is not.
    // Actions never overlap, so a wipe cannot run under an index that would write its files back.
    private func refuseWhileBusy() -> Bool {
        guard let busy else { return true }
        lines = ["\(busy) is still running. Try again when it finishes."]
        return false
    }

    // run marks the model busy with label, runs body on the open core, shows its lines, and
    // reads the status again. A cancelable body can be stopped with cancel.
    private func run(_ label: String, cancelable: Bool = true, _ body: (Core) async throws -> [String]) async {
        guard refuseWhileBusy() else { return }
        guard let core else {
            lines = ["The index is not open."]
            return
        }
        busy = label
        canceled = false
        canCancel = cancelable
        do {
            lines = try await body(core)
        } catch {
            lines = [SearchModel.describe(error)]
        }
        busy = nil
        progress = nil
        canCancel = false
        await loadStatus()
    }

    // cancel stops the run going now and any job still waiting behind it. Nothing a finished job
    // saved is lost.
    public func cancel() {
        guard canCancel else { return }
        canceled = true
        canCancel = false
        progress = "Canceling"
        Task { await keepCanceling() }
    }

    // keepCanceling calls the core's cancel until the run ends, since a call landing in the moment
    // before the run registers with the core reaches nothing. A new run clears canceled, so this
    // never reaches a later one.
    private func keepCanceling() async {
        while canceled, busy != nil {
            core?.cancel()
            try? await Task.sleep(for: .milliseconds(100))
        }
    }

    // showProgress shows line from the core as the newest step, with no path into the app's
    // container. Once a run is canceling, the line stays.
    private func showProgress(_ line: String) {
        guard busy != nil, !canceled else { return }
        progress = line.replacingOccurrences(of: dir.path + "/", with: "")
            .replacingOccurrences(of: dir.path, with: "the data folder")
    }

    // follow sends core's progress lines to the main queue, which keeps them in order.
    private func follow(_ core: Core?) {
        core?.observeProgress { [weak self] line in
            DispatchQueue.main.async {
                MainActor.assumeIsolated { self?.showProgress(line) }
            }
        }
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
        follow(core)
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
