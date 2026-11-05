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
    // detail is the person shown in place of the results, nil while the results show.
    @Published public private(set) var detail: PersonDetail?
    // message says why there is nothing to show, such as an index not built yet.
    @Published public private(set) var message: String?

    // core answers the questions, nil when the data directory did not open.
    private let core: Core?
    // opener opens a link in its own app and reports whether it did.
    private let opener: (URL) -> Bool
    // delay is how long typing must pause before a question is asked.
    private let delay: Duration
    // pending is the debounced question waiting for typing to pause.
    private var pending: Task<Void, Never>?
    // answered is the query the shown results answer.
    private var answered = ""
    // generation counts questions asked, so an older answer never replaces a newer one.
    private var generation = 0

    // init builds a model over core that opens links with opener. A nil core shows openError.
    public init(core: Core?, openError: String? = nil, delay: Duration = .milliseconds(250),
                opener: @escaping (URL) -> Bool) {
        self.core = core
        self.opener = opener
        self.delay = delay
        if core == nil {
            message = openError ?? "The index did not open."
        }
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
            show(people: [], channels: [], message: nil, for: text)
            return
        }
        do {
            let answer = try await core.ask(trimmed, limit: 5)
            guard mine == generation else { return }
            let people = answer.people ?? []
            let channels = answer.channels ?? []
            let none = people.isEmpty && channels.isEmpty ? "No one matches \"\(trimmed)\" yet." : nil
            show(people: people, channels: channels, message: none, for: text)
        } catch {
            guard mine == generation else { return }
            show(people: [], channels: [], message: Self.describe(error), for: text)
        }
    }

    // submit answers the field if its results are stale, then opens the first person's first
    // link. With no link it shows that person instead.
    public func submit() async {
        pending?.cancel()
        if answered != query {
            await ask(query)
        }
        guard let first = people.first else { return }
        await open(first.id)
    }

    // open looks up the person with id and opens their first link, showing them when there is
    // none or it does not open.
    public func open(_ id: String) async {
        guard let person = await lookUp(id) else { return }
        if let link = person.links?.first, let url = URL(string: link), opener(url) {
            return
        }
        detail = person
    }

    // select shows the person with id in place of the results.
    public func select(_ id: String) async {
        guard let person = await lookUp(id) else { return }
        detail = person
    }

    // back returns from a person to the results.
    public func back() {
        detail = nil
    }

    // openLink opens one of the shown person's links.
    public func openLink(_ link: String) {
        guard let url = URL(string: link) else { return }
        if !opener(url) {
            message = "Could not open \(link)."
        }
    }

    // lookUp returns the person with id, or nil after showing why not.
    private func lookUp(_ id: String) async -> PersonDetail? {
        guard let core else { return nil }
        do {
            return try await core.person(id)
        } catch {
            message = Self.describe(error)
            return nil
        }
    }

    // show replaces the result list with the answer to text.
    private func show(people: [AskPerson], channels: [AskChannel], message: String?, for text: String) {
        self.people = people
        self.channels = channels
        self.message = message
        answered = text
    }

    // describe turns a core error into a sentence for the menu.
    static func describe(_ error: Error) -> String {
        let text = (error as NSError).localizedDescription
        guard let first = text.first else { return "Something went wrong." }
        return first.uppercased() + text.dropFirst()
    }
}
