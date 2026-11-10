import Foundation

// ChangeSummary says what changed in the index in short sentences for the menu.
public enum ChangeSummary {
    // shown is how many names a line lists before it counts the rest.
    static let shown = 3

    // lines has one sentence per kind of change, and none when nothing changed.
    public static func lines(_ c: IndexChanges) -> [String] {
        [
            line("Joined", c.peopleJoined),
            line("Left", c.peopleLeft),
            line("New teams", c.teamsAdded),
            line("Teams gone", c.teamsRemoved),
            line("New channels", c.channelsAdded?.map { "#\($0)" }),
            line("Channels gone", c.channelsRemoved?.map { "#\($0)" }),
        ].compactMap { $0 }
    }

    // line is "label: a, b, c and n more.", or nil when names is empty.
    static func line(_ label: String, _ names: [String]?) -> String? {
        guard let names, !names.isEmpty else { return nil }
        let head = names.prefix(shown).joined(separator: ", ")
        let rest = names.count - shown
        return rest > 0 ? "\(label): \(head) and \(rest) more." : "\(label): \(head)."
    }
}
