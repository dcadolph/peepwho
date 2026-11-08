import Foundation

// Member is a person named inside another answer, such as a channel member or a manager.
public struct Member: Codable, Hashable, Sendable {
    // name is the person's display name.
    public let name: String
    // email is the person's address, when known.
    public let email: String?
}

// AskPerson is one person an Ask answer suggests talking to.
public struct AskPerson: Codable, Hashable, Sendable, Identifiable {
    // id is the person's canonical id, which Person takes.
    public let id: String
    // name is the person's display name.
    public let name: String
    // email is the person's address, when known.
    public let email: String?
    // title is the person's job title, when known.
    public let title: String?
    // team is the person's team, when known.
    public let team: String?
    // topics are the person's strongest topics.
    public let topics: [String]?
    // reasons say why the person matched.
    public let reasons: [String]?
    // score is the ranking score.
    public let score: Double
    // strength is the score relative to the best answer, zero to one.
    public let strength: Double
}

// AskChannel is one channel an Ask answer suggests asking in.
public struct AskChannel: Codable, Hashable, Sendable {
    // name is the channel name.
    public let name: String
    // url opens the channel.
    public let url: String?
    // topic is the channel's stated topic.
    public let topic: String?
    // members are the channel's most active members.
    public let members: [Member]?
    // reasons say why the channel matched.
    public let reasons: [String]?
    // score is the ranking score.
    public let score: Double
    // strength is the score relative to the best answer, zero to one.
    public let strength: Double
}

// AskAnswer is who to talk to and where to ask about a question.
public struct AskAnswer: Codable, Sendable {
    // query is the question as asked.
    public let query: String
    // people are the people to talk to, best first.
    public let people: [AskPerson]?
    // channels are the channels to ask in, best first.
    public let channels: [AskChannel]?
}

// SearchResult is one person a name or keyword search matched.
public struct SearchResult: Codable, Hashable, Sendable, Identifiable {
    // id is the person's canonical id.
    public let id: String
    // name is the person's display name.
    public let name: String
    // kind says what matched, such as a name or a topic.
    public let kind: String
    // email is the person's address, when known.
    public let email: String?
    // title is the person's job title, when known.
    public let title: String?
    // team is the person's team, when known.
    public let team: String?
    // topics are the person's strongest topics.
    public let topics: [String]?
    // matched are the terms that matched.
    public let matched: [String]?
    // score is the ranking score.
    public let score: Double
}

// SearchAnswer is the people a search matched.
public struct SearchAnswer: Codable, Sendable {
    // query is the search as typed.
    public let query: String
    // results are the matches, best first.
    public let results: [SearchResult]?
}

// PersonDetail is everything the index knows about one person.
public struct PersonDetail: Codable, Sendable, Identifiable {
    // id is the person's canonical id.
    public let id: String
    // name is the person's display name.
    public let name: String
    // email is the person's address, when known.
    public let email: String?
    // title is the person's job title, when known.
    public let title: String?
    // team is the person's team, when known.
    public let team: String?
    // manager is the person's manager, when known.
    public let manager: Member?
    // topics are the person's topics, strongest first.
    public let topics: [String]?
    // channels are the channels the person is active in.
    public let channels: [String]?
    // identities are the other ids merged into this person.
    public let identities: [String]?
    // links open a conversation with the person in a source's own app, such as slack://user.
    public let links: [String]?
}

// EpisodePerson is someone who took part in a conversation.
public struct EpisodePerson: Codable, Hashable, Sendable {
    // id is the person's canonical id, when known.
    public let id: String?
    // name is the person's display name, when known.
    public let name: String?
    // email is the person's address, when known.
    public let email: String?
    // title is the person's job title, when known.
    public let title: String?
}

// Episode is one conversation a person took part in.
public struct Episode: Codable, Hashable, Sendable {
    // id identifies the conversation, when the source gives one.
    public let id: String?
    // kind says what sort of conversation it was, such as a thread.
    public let kind: String
    // source is the tool it came from.
    public let source: String
    // place is where it happened, such as a channel.
    public let place: String?
    // when is when it happened, RFC 3339.
    public let when: String
    // people are who took part.
    public let people: [EpisodePerson]
    // messages is how many messages it holds, when counted.
    public let messages: Int?
    // permalink opens the conversation.
    public let permalink: String?
    // linkMayHaveExpired reports a permalink the source may no longer serve.
    public let linkMayHaveExpired: Bool?
    // matched are the query terms it matched.
    public let matched: [String]?
    // strength is the match relative to the best one, zero to one.
    public let strength: Double

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case id, kind, source, place, when, people, messages, permalink, matched, strength
        case linkMayHaveExpired = "link_may_have_expired"
    }
}

// RecallScope says what the conversation store held when a recall ran.
public struct RecallScope: Codable, Sendable {
    // episodes is how many conversations the store holds.
    public let episodes: Int
    // oldest is the oldest conversation's time, RFC 3339.
    public let oldest: String?
    // sources are the tools the conversations came from.
    public let sources: [String]
    // note explains what the answer covers.
    public let note: String
}

// RecallAnswer is the conversations a person took part in about a query.
public struct RecallAnswer: Codable, Sendable {
    // person is the person recalled.
    public let person: String
    // query is the query as asked.
    public let query: String
    // episodes are the matching conversations, best first.
    public let episodes: [Episode]?
    // scope says what the store held.
    public let scope: RecallScope
}

// IndexStatus is what the index holds and how old it is.
public struct IndexStatus: Codable, Sendable {
    // people is how many people the index holds.
    public let people: Int
    // teams is how many teams the index holds.
    public let teams: Int
    // subjects is how many subjects the index holds.
    public let subjects: Int
    // channels is how many channels the index holds.
    public let channels: Int
    // words is how many distinct words the index holds.
    public let words: Int
    // conversations is how many conversations the store holds.
    public let conversations: Int
    // sources counts records per source.
    public let sources: [String: Int]
    // embeddings reports whether the index carries embeddings.
    public let embeddings: Bool
    // sealed reports whether the index is encrypted at rest.
    public let sealed: Bool
    // builtAt is when the index was written, RFC 3339, absent before the first index.
    public let builtAt: String?
    // ageSeconds is how long before the supplied clock the index was written.
    public let ageSeconds: Int64
    // scopes are the recorded index runs in source then scope order.
    public let scopes: [IndexScope]

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case people, teams, subjects, channels, words, conversations, sources, embeddings, sealed, scopes
        case builtAt = "built_at"
        case ageSeconds = "age_seconds"
    }
}

// IndexScope is one recorded index run: when it last worked and how its last try went.
public struct IndexScope: Codable, Sendable {
    // source is the source the run reads.
    public let source: String
    // scope is what the run reads, such as repo:/path.
    public let scope: String
    // ranAt is when it last worked, RFC 3339, absent when not known.
    public let ranAt: String?
    // ageSeconds is how long before the supplied clock ranAt was, zero when ranAt is absent.
    public let ageSeconds: Int64
    // attemptedAt is when it was last tried, RFC 3339, absent when not known.
    public let attemptedAt: String?
    // error says why the last try failed, absent when it worked.
    public let error: String?

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case source, scope, error
        case ranAt = "ran_at"
        case ageSeconds = "age_seconds"
        case attemptedAt = "attempted_at"
    }
}

// IndexChanges is who and what joined or left the index.
public struct IndexChanges: Codable, Sendable {
    // peopleJoined are people present now but not before.
    public let peopleJoined: [String]?
    // peopleLeft are people present before but not now.
    public let peopleLeft: [String]?
    // teamsAdded are teams present now but not before.
    public let teamsAdded: [String]?
    // teamsRemoved are teams present before but not now.
    public let teamsRemoved: [String]?
    // channelsAdded are channels present now but not before.
    public let channelsAdded: [String]?
    // channelsRemoved are channels present before but not now.
    public let channelsRemoved: [String]?

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case peopleJoined = "people_joined"
        case peopleLeft = "people_left"
        case teamsAdded = "teams_added"
        case teamsRemoved = "teams_removed"
        case channelsAdded = "channels_added"
        case channelsRemoved = "channels_removed"
    }

    // isEmpty reports whether nothing changed.
    public var isEmpty: Bool {
        [peopleJoined, peopleLeft, teamsAdded, teamsRemoved, channelsAdded, channelsRemoved]
            .allSatisfy { ($0 ?? []).isEmpty }
    }
}

// ChangesAnswer is what changed after a point in time, merged into one set.
public struct ChangesAnswer: Codable, Sendable {
    // asOf is when the newest counted change ran, RFC 3339, absent when none.
    public let asOf: String?
    // entries is how many logged runs the answer merges.
    public let entries: Int
    // changes is the net of those runs.
    public let changes: IndexChanges

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case asOf = "as_of"
        case entries, changes
    }
}

// IndexResult is the outcome of indexing one source.
public struct IndexResult: Codable, Sendable {
    // source is the source indexed.
    public let source: String
    // scope is what was read, such as repo:/path.
    public let scope: String
    // log is the run's progress text.
    public let log: String
}

// RefreshScope is the outcome of replaying one recorded scope.
public struct RefreshScope: Codable, Sendable {
    // source is the source replayed.
    public let source: String
    // scope is what was read.
    public let scope: String
    // ranAt is when it ran, RFC 3339.
    public let ranAt: String
    // ok reports whether it succeeded.
    public let ok: Bool
    // error says why it failed, when it did.
    public let error: String?

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case source, scope, ok, error
        case ranAt = "ran_at"
    }
}

// RefreshResult is the outcome of a refresh across every recorded scope.
public struct RefreshResult: Codable, Sendable {
    // ok reports whether every scope succeeded.
    public let ok: Bool
    // results are the scopes, one each.
    public let results: [RefreshScope]
    // log is the run's progress text.
    public let log: String
}

// WipeResult lists what a wipe removed.
public struct WipeResult: Codable, Sendable {
    // removed are the files deleted.
    public let removed: [String]?
}
