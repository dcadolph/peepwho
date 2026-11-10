import Foundation

// SearchModel drives the menu bar search: typing asks who to talk to, a row shows that person,
// and Enter opens a conversation with the first person in their own app.
@MainActor
public final class SearchModel: ObservableObject {
    // query is the text in the search field.
    @Published public var query = ""
    // people are the people the last answered query suggests, best first.
    @Published public private(set) var people: [AskPerson] = []
    // channels are the channels the last answered query suggests, best first.
    @Published public private(set) var channels: [AskChannel] = []
    // teams are the owners of record the last answered query names. They are shown, never opened.
    @Published public private(set) var teams: [AskTeam] = []
    // detail is the person shown in place of the results, nil while the results show.
    @Published public private(set) var detail: PersonDetail?
    // message says why there is nothing to show, such as an index not built yet.
    @Published public private(set) var message: String?
    // changes say who and what joined or left since the menu last closed.
    @Published public private(set) var changes: [String] = []
    // sample says the index holds the sample company, so the window can say so.
    @Published public var sample = false
    // refreshFailure says which sources failed their last refresh, nil when none did.
    @Published public var refreshFailure: String?

    // core answers the questions, nil when the data directory did not open.
    private var core: Core?
    // opener opens a link in its own app and reports whether it did.
    private let opener: (URL) -> Bool
    // delay is how long typing must pause before a question is asked.
    private let delay: Duration
    // defaults keeps the last look across launches.
    private let defaults: UserDefaults
    // pending is the debounced question waiting for typing to pause.
    private var pending: Task<Void, Never>?
    // answered is the query the shown results answer.
    private var answered = ""
    // generation counts questions asked and people looked up, so an older answer never replaces
    // what the window moved on to.
    private var generation = 0
    // lookedRead reports whether the last open read the changes, so a close never moves the last
    // look past changes nobody saw.
    private var lookedRead = false

    // init builds a model over core that opens links with opener. A nil core shows openError.
    public init(core: Core?, openError: String? = nil, delay: Duration = .milliseconds(250),
                defaults: UserDefaults = .standard, opener: @escaping (URL) -> Bool) {
        self.core = core
        self.opener = opener
        self.delay = delay
        self.defaults = defaults
        if core == nil {
            message = openError ?? "The index did not open."
        }
    }

    // use swaps in core, such as the fresh one a wipe opens, and clears what the old one answered.
    public func use(_ core: Core?, openError: String? = nil) {
        self.core = core
        pending?.cancel()
        pending = nil
        generation += 1
        people = []
        channels = []
        teams = []
        detail = nil
        changes = []
        answered = ""
        message = core == nil ? (openError ?? "The index is not open.") : nil
    }

    // lastLookKey is the UserDefaults key for when the menu last closed.
    public static let lastLookKey = "WhoToAskLastLook"

    // opened shows what changed since the last look. The first open ever starts the clock and
    // shows nothing, since everything would count as new. A read that fails starts nothing and
    // moves nothing, so what changed still shows on the next open.
    public func opened(now: Date = Date()) async {
        lookedRead = false
        guard let core else { return }
        let last = defaults.object(forKey: Self.lastLookKey) as? Date
        guard let answer = try? await core.changes(since: last ?? now) else { return }
        lookedRead = true
        if last == nil {
            defaults.set(now, forKey: Self.lastLookKey)
        }
        changes = last == nil ? [] : ChangeSummary.lines(answer.changes)
    }

    // closed moves the last look to now, if the open that this close ends read the changes.
    public func closed(now: Date = Date()) {
        guard lookedRead else { return }
        defaults.set(now, forKey: Self.lastLookKey)
    }

    // typed asks about text once typing pauses, replacing any question still waiting.
    public func typed(_ text: String) {
        pending?.cancel()
        pending = Task { [delay] in
            try? await Task.sleep(for: delay)
            guard !Task.isCancelled else { return }
            await self.ask(text)
        }
    }

    // ask answers text now and shows the result list.
    public func ask(_ text: String) async {
        generation += 1
        let mine = generation
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        detail = nil
        guard let core else { return }
        guard !trimmed.isEmpty else {
            show(people: [], channels: [], teams: [], message: nil, for: text)
            return
        }
        do {
            let answer = try await core.ask(trimmed, limit: 5)
            guard mine == generation else { return }
            let people = answer.people ?? []
            let channels = answer.channels ?? []
            let teams = answer.teams ?? []
            let empty = people.isEmpty && channels.isEmpty && teams.isEmpty
            let none = empty ? "No one matches \"\(trimmed)\" yet." : nil
            show(people: people, channels: channels, teams: teams, message: none, for: text)
        } catch {
            guard mine == generation else { return }
            show(people: [], channels: [], teams: [], message: Self.describe(error), for: text)
        }
    }

    // submit answers the field if its results are stale, then opens the first person's first
    // link. With no link it shows that person instead. A team is never the target: with no
    // person in the answer, Enter does nothing.
    public func submit() async {
        pending?.cancel()
        if answered != query {
            await ask(query)
        }
        guard let first = people.first else { return }
        await open(first.id)
    }

    // open looks up the person with id and opens their first link the app allows, showing them
    // when there is none or it does not open.
    public func open(_ id: String) async {
        let mine = nextGeneration()
        guard let person = await lookUp(id, mine), mine == generation else { return }
        if let url = person.links?.lazy.compactMap(Self.openable).first, opener(url) {
            return
        }
        show(person)
    }

    // select shows the person with id in place of the results.
    public func select(_ id: String) async {
        let mine = nextGeneration()
        guard let person = await lookUp(id, mine), mine == generation else { return }
        show(person)
    }

    // back returns from a person to the results, and a lookup still running stays out of it.
    public func back() {
        generation += 1
        detail = nil
        message = nil
    }

    // openLink opens one of the shown links, or says it could not.
    public func openLink(_ link: String) {
        guard let url = Self.openable(link), opener(url) else {
            message = "Could not open \(link)."
            return
        }
        message = nil
    }

    // openableSchemes are the only link schemes the app opens, since links come from servers and
    // Enter opens without asking.
    private static let openableSchemes: Set<String> = ["https", "http", "slack", "mailto"]

    // openable returns link as a URL when its scheme is one the app opens, and nil otherwise.
    public static func openable(_ link: String) -> URL? {
        guard let url = URL(string: link), let scheme = url.scheme?.lowercased(),
              openableSchemes.contains(scheme) else { return nil }
        return url
    }

    // nextGeneration starts a new step in the window and returns its number.
    private func nextGeneration() -> Int {
        generation += 1
        return generation
    }

    // lookUp returns the person with id, or nil after showing why not, if step mine is still current.
    private func lookUp(_ id: String, _ mine: Int) async -> PersonDetail? {
        guard let core else { return nil }
        do {
            return try await core.person(id)
        } catch {
            if mine == generation {
                message = Self.describe(error)
            }
            return nil
        }
    }

    // show puts person in place of the results and clears any older message.
    private func show(_ person: PersonDetail) {
        detail = person
        message = nil
    }

    // show replaces the result list with the answer to text.
    private func show(people: [AskPerson], channels: [AskChannel], teams: [AskTeam], message: String?,
                      for text: String) {
        self.people = people
        self.channels = channels
        self.teams = teams
        self.message = message
        answered = text
    }

    // noIndex says where to go when nothing is indexed yet.
    static let noIndex = "Nothing is indexed yet. Open Settings to pick a folder, add a token, or load the sample company."

    // describe turns a core error into a sentence for the window: no internal prefix, no path into
    // the app's container, a capital, and an ending. dataDir is where the app keeps its index.
    static func describe(_ error: Error, dataDir: URL = DataDir.url) -> String {
        var text = (error as NSError).localizedDescription
            .replacingOccurrences(of: dataDir.path + "/", with: "")
            .replacingOccurrences(of: dataDir.path, with: "the data folder")
        if text.hasPrefix("core: no index yet") {
            return noIndex
        }
        if text.hasPrefix("core: ") {
            text.removeFirst("core: ".count)
        }
        guard let first = text.first else { return "Something went wrong." }
        let ending = text.last.map { ".!?".contains($0) } == true ? "" : "."
        return first.uppercased() + text.dropFirst() + ending
    }
}
