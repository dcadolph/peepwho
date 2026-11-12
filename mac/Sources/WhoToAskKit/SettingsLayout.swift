import Foundation

// SettingsTab is one tab of the settings window, in the order the window shows them.
public enum SettingsTab: String, CaseIterable, Identifiable, Sendable {
    // general holds opening at login, the version, and About.
    case general
    // sources holds one card per place WhoToAsk reads.
    case sources
    // index holds what the index holds and the actions that build it.
    case index
    // privacy holds how the index is kept and the actions that remove it.
    case privacy

    // id names the tab.
    public var id: String { rawValue }

    // title is the tab's name in the window.
    public var title: String {
        switch self {
        case .general: "General"
        case .sources: "Sources"
        case .index: "Index"
        case .privacy: "Privacy"
        }
    }

    // symbol is the tab's SF Symbol.
    public var symbol: String {
        switch self {
        case .general: "gearshape"
        case .sources: "tray.full"
        case .index: "magnifyingglass"
        case .privacy: "lock.shield"
        }
    }
}

// SourceCard is one source on the Sources tab: what it is called and what it needs to run.
public struct SourceCard: Identifiable, Equatable, Sendable {
    // title is the card's heading.
    public let title: String
    // symbol is the card's SF Symbol.
    public let symbol: String
    // source is the index source a token card runs, empty for the places card.
    public let source: String
    // secrets are the secret names the card asks for, in field order.
    public let secrets: [String]
    // slots are the places the card picks, in row order.
    public let slots: [BookmarkSlot]

    // id names the card.
    public var id: String { title }

    // all lists every card in the order the tab shows them. Folders and files leads because it needs no
    // token, so it is where every first run starts.
    public static let all: [SourceCard] = [
        SourceCard(title: "Folders and files", symbol: "folder", source: "", secrets: [], slots: [.git, .csv, .codeowners]),
        SourceCard(title: "Slack", symbol: "number", source: "slack", secrets: [SecretName.slackToken], slots: []),
        SourceCard(title: "GitHub", symbol: "chevron.left.forwardslash.chevron.right", source: "github",
                   secrets: [SecretName.githubToken], slots: []),
        SourceCard(title: "Jira", symbol: "checklist", source: "jira",
                   secrets: [SecretName.jiraURL, SecretName.jiraEmail, SecretName.jiraToken], slots: []),
        SourceCard(title: "Confluence", symbol: "doc.text", source: "confluence",
                   secrets: [SecretName.confluenceURL, SecretName.confluenceEmail, SecretName.confluenceToken], slots: []),
        SourceCard(title: "PagerDuty", symbol: "bell", source: "pagerduty", secrets: [SecretName.pagerdutyToken], slots: []),
    ]

    // pick returns the card titled title, or the first card when no card has that title.
    public static func pick(_ title: String?) -> SourceCard {
        all.first { $0.title == title } ?? all[0]
    }

    // entered lists every secret a person types into a card.
    public static var entered: [String] { all.flatMap(\.secrets) }

    // state says whether the card holds what its source needs: every required secret and a scope
    // for a token card, and at least one picked place for the places card.
    public func state(saved: Set<String>, placed: Set<BookmarkSlot>, scopes: Scopes) -> CardState {
        if !slots.isEmpty {
            return slots.contains(where: placed.contains) ? .setUp : .notSetUp
        }
        guard SecretName.required(by: source).allSatisfy(saved.contains) else { return .notSetUp }
        if let need = scopes.need(source) {
            return .needs(need.badge)
        }
        return .setUp
    }
}

// CardState is what a source card says beside its title.
public enum CardState: Equatable, Sendable {
    // notSetUp is a token card missing a secret its source needs, or the places card with nothing
    // picked.
    case notSetUp
    // needs is a token card with its secrets saved and nothing to read, holding what to add, such
    // as "Needs a repository".
    case needs(String)
    // setUp is a card Index Now reads.
    case setUp
}
