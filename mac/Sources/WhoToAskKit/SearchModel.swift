import Foundation

// SearchModel drives the menu bar search: typing asks who to talk to, a row shows that person,
// the arrow keys mark a row, and Enter opens a conversation with the marked person in their own
// app.
@MainActor
public final class SearchModel: ObservableObject {
    // query is the text in the search field.
    @Published public var query = ""
    // people are the people the last answered query suggests, best first, with the deactivated
    // ones last.
    @Published public private(set) var people: [AskPerson] = []
    // channels are the channels the last answered query suggests, best first.
    @Published public private(set) var channels: [AskChannel] = []
    // teams are the owners of record the last answered query names. They are shown, never opened.
    @Published public private(set) var teams: [AskTeam] = []
    // closest are the people whose name or details hold the question, shown under "Closest by
    // name" when the answer names no one, such as a colleague asked for by first name alone.
    @Published public private(set) var closest: [SearchResult] = []
    // detail is the person shown in place of the results, nil while the results show.
    @Published public private(set) var detail: PersonDetail?
    // selected is the index into people of the row the arrow keys mark, nil when there is no one.
    // A new answer marks the first row.
    @Published public private(set) var selected: Int?
    // message says why there is nothing to show, such as an index not built yet.
    @Published public private(set) var message: String?
    // changes say who and what joined or left since the menu last closed.
    @Published public private(set) var changes: [String] = []
    // sample says the index holds the sample company, so the window can say so.
    @Published public var sample = false
    // empty says the open index holds no one yet, so the window can point to Settings.
    @Published public var empty = false
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
    private(set) var answered = ""
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

    // use swaps in core, such as the fresh one a wipe opens, and clears the field and what the
    // old one answered, so no question shows without its answer.
    public func use(_ core: Core?, openError: String? = nil) {
        self.core = core
        pending?.cancel()
        pending = nil
        generation += 1
        query = ""
        people = []
        channels = []
        teams = []
        closest = []
        detail = nil
        selected = nil
        changes = []
        answered = ""
        message = core == nil ? (openError ?? "The index is not open.") : nil
    }

    // lastLookKey is the UserDefaults key for when the menu last closed.
    public static let lastLookKey = "WhoToAskLastLook"

    // opened answers the shown question again, since the index may have changed while the window
    // was closed, then shows what changed since the last look. The first open ever starts the
    // clock and shows nothing, since everything would count as new. A read that fails starts
    // nothing and moves nothing, so what changed still shows on the next open.
    public func opened(now: Date = Date()) async {
        lookedRead = false
        guard let core else { return }
        if !answered.isEmpty {
            await reask()
        }
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
            // The departed close the people list, so the ranking reads as one list with them at
            // the bottom, each marked.
            let people = (answer.people ?? []) + (answer.departed ?? [])
            let channels = answer.channels ?? []
            let teams = answer.teams ?? []
            let empty = people.isEmpty && channels.isEmpty && teams.isEmpty
            var nearest: [SearchResult] = []
            if empty {
                nearest = await nameMatches(trimmed)
                guard mine == generation else { return }
            }
            let none = empty && nearest.isEmpty ? "No one matches \"\(trimmed)\" yet." : nil
            show(people: people, channels: channels, teams: teams, closest: nearest, message: none,
                 for: text)
        } catch {
            guard mine == generation else { return }
            show(people: [], channels: [], teams: [], message: Self.describe(error), for: text)
        }
    }

    // nameMatches finds up to five people whose name, email, title, team, or topics hold text,
    // best first, through the name search the command line falls back to. It leaves out teams and
    // channels, which have no page to show, and returns none when the search fails, since the
    // window then says no one matches, which is still true of the answer.
    private func nameMatches(_ text: String) async -> [SearchResult] {
        guard let core, let found = try? await core.search(text, limit: 5) else { return [] }
        return (found.results ?? []).filter { $0.kind == "person" }
    }

    // reask answers the field again from the index as it now stands, such as after an index run
    // or a sample load. It does nothing while a person shows or the field is empty, and a question
    // still waiting for typing to pause is asked now in its place.
    public func reask() async {
        guard detail == nil, !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        pending?.cancel()
        pending = nil
        await ask(query)
    }

    // submit acts on the person shown, if the field still holds the question that led there, by
    // opening their first contact link, and does nothing when they have none. Otherwise it
    // answers the field if its results are stale, then opens the marked person's first link,
    // showing that person when there is no link. A team is never the target: with no person in
    // the answer, Enter does nothing.
    public func submit() async {
        pending?.cancel()
        if let detail, answered == query {
            let links = Self.contactLinks(detail.links ?? [], deactivated: detail.isDeactivated)
            if let link = links.first {
                openLink(link)
            }
            return
        }
        if answered != query {
            await ask(query)
        }
        guard let target = marked else { return }
        await open(target.id)
    }

    // marked is the person the marked row shows, nil when there is no one to mark.
    public var marked: AskPerson? {
        guard let selected, people.indices.contains(selected) else { return nil }
        return people[selected]
    }

    // moveDown marks the next row, stays on the last one, and returns the person it marked, nil
    // when the mark did not move.
    @discardableResult
    public func moveDown() -> AskPerson? {
        guard detail == nil, let selected else { return nil }
        return mark(min(selected + 1, people.count - 1))
    }

    // moveUp marks the row before, stays on the first one, and returns the person it marked, nil
    // when the mark did not move.
    @discardableResult
    public func moveUp() -> AskPerson? {
        guard detail == nil, let selected else { return nil }
        return mark(max(selected - 1, 0))
    }

    // mark marks row and returns its person, or nil when row was marked already, so the window
    // speaks a name only when the mark moved.
    private func mark(_ row: Int) -> AskPerson? {
        guard row != selected else { return nil }
        selected = row
        return marked
    }

    // strengthValue says a match strength from zero to one as a whole percent, such as
    // "83 percent", for VoiceOver to read beside the bar.
    public static func strengthValue(_ strength: Double) -> String {
        "\(Int((max(0, min(1, strength)) * 100).rounded())) percent"
    }

    // showMarked shows the marked person in place of the results, which Command-Return does. As
    // with Enter, a question still waiting for typing to pause is asked first, so the person shown
    // is from the answer to what the field holds.
    public func showMarked() async {
        guard detail == nil else { return }
        pending?.cancel()
        if answered != query {
            await ask(query)
        }
        guard let target = marked else { return }
        await select(target.id)
    }

    // escape goes back from a person to the results and reports whether it did, so the key does
    // whatever it did before everywhere else.
    public func escape() -> Bool {
        guard detail != nil else { return false }
        back()
        return true
    }

    // enterHint says what Enter does in the window as it stands, nil when it does nothing. It
    // promises a message only for a person with a link the app opens, and says Enter shows the
    // person otherwise.
    public var enterHint: String? {
        guard let detail else {
            let nobody = query.isEmpty || (answered == query && people.isEmpty)
            if nobody {
                return nil
            }
            // Stale results are answered again on Enter, which marks the first row anew, so who
            // that is, and whether they can be messaged, is not known yet.
            guard answered == query, let selected, let target = marked else {
                return "Enter opens the first person."
            }
            let verb = target.isReachable ? "messages" : "shows"
            return "Enter \(verb) \(selected > 0 ? target.name : "the first person")."
        }
        let links = Self.contactLinks(detail.links ?? [], deactivated: detail.isDeactivated)
        return links.isEmpty ? nil : "Enter messages this person."
    }

    // open looks up the person with id and opens their first link the app allows, showing them
    // when there is none or it does not open.
    public func open(_ id: String) async {
        let mine = nextGeneration()
        guard let person = await lookUp(id, mine), mine == generation else { return }
        // A deactivated person has no Slack link to follow, so they are shown instead.
        let links = Self.contactLinks(person.links ?? [], deactivated: person.isDeactivated)
        if let url = links.lazy.compactMap(Self.openable).first, opener(url) {
            return
        }
        show(person)
    }

    // select shows the person with id in place of the results, and marks their row when they are
    // in the results, so a click moves the mark the way the arrow keys do.
    public func select(_ id: String) async {
        if let row = people.firstIndex(where: { $0.id == id }) {
            selected = row
        }
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

    // contactLinks returns the links worth a button: the ones the app opens, without any
    // slack: link for a deactivated person, since a message there can no longer reach them.
    public static func contactLinks(_ links: [String], deactivated: Bool) -> [String] {
        links.filter { link in
            openable(link) != nil && !(deactivated && link.lowercased().hasPrefix("slack:"))
        }
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
    private func show(people: [AskPerson], channels: [AskChannel], teams: [AskTeam],
                      closest: [SearchResult] = [], message: String?, for text: String) {
        self.people = people
        self.channels = channels
        self.teams = teams
        self.closest = closest
        self.message = message
        selected = people.isEmpty ? nil : 0
        answered = text
    }

    // noIndex says where to go when nothing is indexed yet.
    public static let noIndex = "Nothing is indexed yet. Open Settings to pick a folder, add a token, or load the sample company."

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
