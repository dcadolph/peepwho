import Foundation

// Count writes counts in plain words.
public enum Count {
    // words writes n with the singular or plural noun, such as "1 person" or "6 people".
    public static func words(_ n: Int, _ one: String, _ many: String) -> String {
        "\(n) \(n == 1 ? one : many)"
    }

    // list joins parts the way a sentence lists them: "a", "a and b", or "a, b, and c".
    public static func list(_ parts: [String]) -> String {
        switch parts.count {
        case 0: ""
        case 1: parts[0]
        case 2: "\(parts[0]) and \(parts[1])"
        default: "\(parts.dropLast().joined(separator: ", ")), and \(parts[parts.count - 1])"
        }
    }
}

// SourceLabel names sources the way a person knows them.
public enum SourceLabel {
    // of is the name a person knows source by, or source itself for one it does not know.
    public static func of(_ source: String) -> String {
        switch source {
        case "git": "Git"
        case "org-csv": "Org chart CSV"
        case "codeowners": "CODEOWNERS"
        case "slack": "Slack"
        case "github": "GitHub"
        case "jira": "Jira"
        case "confluence": "Confluence"
        case "pagerduty": "PagerDuty"
        default: source
        }
    }
}

// Found is what one run of a source read.
public struct Found: Codable, Equatable, Sendable {
    // repos is how many repositories the run read, zero for a source other than git.
    public let repos: Int
    // people is how many people in the index hold a record from the source after the run,
    // counted after identities are joined.
    public let people: Int
    // channels is how many channels the source holds after the run.
    public let channels: Int

    // who says whom the run found: "found 5 people in 1 channel", "found 1 person", or
    // "found no one".
    var who: String {
        let found = people == 0 ? "found no one" : "found \(Count.words(people, "person", "people"))"
        return channels > 0 ? "\(found) in \(Count.words(channels, "channel", "channels"))" : found
    }

    // sentence says what source's runs found as a sentence: "Read 58 repositories and found 1
    // person." for git, "Found 5 people in 1 channel." for the rest.
    func sentence(_ source: String) -> String {
        if source == "git" {
            return "Read \(Count.words(repos, "repository", "repositories")) and \(who)."
        }
        return who.prefix(1).uppercased() + who.dropFirst() + "."
    }
}

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
    // deactivated is true when every account the index knows for the person is deactivated,
    // absent otherwise.
    public var deactivated: Bool? = nil
    // reachable is true when the person has a link the app opens to message them, absent
    // otherwise.
    public var reachable: Bool? = nil

    // isDeactivated reports whether the person can no longer be reached.
    public var isDeactivated: Bool { deactivated ?? false }

    // isReachable reports whether Enter on the person opens a conversation rather than their page.
    public var isReachable: Bool { reachable ?? false }

    // role is the person's role line, empty when there is nothing to show.
    public var role: String { Role.line(title: title, team: team, deactivated: isDeactivated) }
}

// Role builds the line under a person's name.
public enum Role {
    // mark is the word that leads the line for a person who can no longer be reached.
    public static let mark = "Deactivated"

    // line joins title and team, led by mark for a person who can no longer be reached so a
    // narrow window cuts the role and never the mark. It is empty when none of those apply.
    public static func line(title: String?, team: String?, deactivated: Bool) -> String {
        [deactivated ? mark : nil, title, team].compactMap { $0 }.filter { !$0.isEmpty }
            .joined(separator: ", ")
    }

    // split returns the mark apart from the rest of line, so the window can color the mark. The
    // mark is nil and rest is all of line when the person is active.
    public static func split(_ line: String, deactivated: Bool) -> (mark: String?, rest: String) {
        guard deactivated, line.hasPrefix(mark) else { return (nil, line) }
        return (mark, String(line.dropFirst(mark.count)))
    }
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

// AskTeam is one team of record an Ask answer names: a CODEOWNERS handle that owns what was
// asked. It is somewhere to look, not somebody to message, so it has no profile and no link.
public struct AskTeam: Codable, Hashable, Sendable, Identifiable {
    // id is the team's identifier, such as codeowners:simcorp/legal-squad.
    public let id: String
    // name is the handle as CODEOWNERS writes it.
    public let name: String
    // team is the org chart team the handle names, when exactly one matches.
    public let team: String?
    // topics are the areas the team owns.
    public let topics: [String]?
    // reasons say why the team matched.
    public let reasons: [String]?
    // score is the ranking score among the teams.
    public let score: Double
    // strength is how strongly the team matches, zero to one.
    public let strength: Double?
}

// AskAnswer is who to talk to and where to ask about a question.
public struct AskAnswer: Codable, Sendable {
    // query is the question as asked.
    public let query: String
    // people are the people to talk to, best first. They never include a team or a
    // deactivated person.
    public let people: [AskPerson]?
    // departed are the deactivated people who match, ranked among themselves and shown below
    // the people.
    public let departed: [AskPerson]?
    // channels are the channels to ask in, best first.
    public let channels: [AskChannel]?
    // teams are the owners of record, ranked apart from the people.
    public let teams: [AskTeam]?
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
    // deactivated is true when every account the index knows for the person is deactivated,
    // absent otherwise.
    public var deactivated: Bool? = nil

    // isDeactivated reports whether the person can no longer be reached.
    public var isDeactivated: Bool { deactivated ?? false }

    // role is the person's role line, empty when there is nothing to show.
    public var role: String { Role.line(title: title, team: team, deactivated: isDeactivated) }
}

// ListedPerson is one person in the list of everyone the index holds.
public struct ListedPerson: Codable, Hashable, Sendable, Identifiable {
    // id is the person's canonical id, which Person takes.
    public let id: String
    // name is the person's display name.
    public let name: String
    // title is the person's job title, empty when not known.
    public let title: String
    // team is the name of the person's team, empty when not known.
    public let team: String
    // links open a conversation with the person in a source's own app.
    public let links: [String]
    // deactivated is true when every account the index knows for the person is deactivated,
    // absent otherwise.
    public var deactivated: Bool? = nil

    // isDeactivated reports whether the person can no longer be reached.
    public var isDeactivated: Bool { deactivated ?? false }

    // role is the person's role line, empty when there is nothing to show.
    public var role: String { Role.line(title: title, team: team, deactivated: isDeactivated) }

    // matches reports whether text is in the name, title, or team, ignoring case. Empty text
    // matches everyone.
    public func matches(_ text: String) -> Bool {
        let text = text.trimmingCharacters(in: .whitespaces)
        guard !text.isEmpty else { return true }
        return [name, title, team].contains { $0.localizedCaseInsensitiveContains(text) }
    }
}

// PeopleList is everyone the index holds, sorted by name.
public struct PeopleList: Codable, Sendable {
    // people are the people, sorted by name.
    public let people: [ListedPerson]
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
    // sample reports whether the index holds the sample company.
    public let sample: Bool
    // loadedAt is when the sample company was loaded, RFC 3339, absent when it was not.
    public let loadedAt: String?
    // deactivated is how many of the people have every known account deactivated, absent
    // when none do.
    public let deactivated: Int?

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case people, teams, subjects, channels, words, conversations, sources, embeddings, sealed, scopes, sample
        case deactivated
        case loadedAt = "loaded_at"
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
    // found is what the last successful run read, absent for a run recorded before runs kept it.
    public let found: Found?

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case source, scope, error, found
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
    // repos is how many repositories the run read, zero for a source other than git.
    public let repos: Int
    // people is how many people the index holds after the run.
    public let people: Int
    // log is the run's progress text.
    public let log: String
    // found is what the run found, counted the way the Index tab's source rows count it, nil from
    // a core that did not report it.
    public var found: Found? = nil

    // finishLine says in plain words what the run read and found, in the words the Index tab's
    // source rows use. Without found it says what the index now holds instead.
    public var finishLine: String {
        if let found {
            if source == "git" {
                return "Git: read \(Count.words(found.repos, "repository", "repositories")) and \(found.who)."
            }
            return "\(SourceLabel.of(source)): \(found.who)."
        }
        let held = "The index holds \(Count.words(people, "person", "people"))."
        if source == "git" {
            return "Git: read \(Count.words(repos, "repository", "repositories")). \(held)"
        }
        return "\(SourceLabel.of(source)): indexed. \(held)"
    }
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
    // found is what it read, absent when it failed.
    public let found: Found?

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case source, scope, ok, error, found
        case ranAt = "ran_at"
    }

    // line says in plain words how the scope went, mirroring IndexResult.finishLine: what git read
    // again and who each source found, or why it failed.
    public var line: String {
        let name = SourceLabel.of(source)
        guard ok else { return "\(name): \(error ?? "failed")." }
        guard let found else { return "\(name): refreshed." }
        if source == "git" {
            return "\(name): read \(Count.words(found.repos, "repository", "repositories")) again and \(found.who)."
        }
        return "\(name): refreshed and \(found.who)."
    }
}

// RefreshResult is the outcome of a refresh across every recorded scope.
public struct RefreshResult: Codable, Sendable {
    // ok reports whether every scope succeeded.
    public let ok: Bool
    // canceled reports that the person stopped the run before every scope ran.
    public let canceled: Bool?
    // results are the scopes, one each.
    public let results: [RefreshScope]
    // log is the run's progress text.
    public let log: String
}

// SampleResult is what loading the sample company put in the index.
public struct SampleResult: Codable, Sendable {
    // people is how many people the sample holds.
    public let people: Int
    // channels is how many channels the sample holds.
    public let channels: Int
    // sources is how many sources the sample was built from.
    public let sources: Int
    // loadedAt is when the sample was loaded, RFC 3339.
    public let loadedAt: String

    // CodingKeys maps the core's snake case names.
    enum CodingKeys: String, CodingKey {
        case people, channels, sources
        case loadedAt = "loaded_at"
    }
}

// WipeResult lists what a wipe removed.
public struct WipeResult: Codable, Sendable {
    // removed are the files deleted.
    public let removed: [String]?
}
