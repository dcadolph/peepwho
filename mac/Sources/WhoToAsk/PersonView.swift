import WhoToAskKit
import SwiftUI

// PersonView shows everything the index knows about one person, with a way back to the results.
struct PersonView: View {
    // person is the person shown.
    let person: PersonDetail
    // model opens links, goes back, and says when a link would not open.
    @ObservedObject var model: SearchModel

    // body lays out the name, role, manager, topics, channels, and links.
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button { model.back() } label: { Label("Results", systemImage: "chevron.left") }
                .buttonStyle(.link)
            NameText(name: person.name, deactivated: person.isDeactivated).font(.title3.weight(.semibold))
            if !person.role.isEmpty {
                RoleText(line: person.role, deactivated: person.isDeactivated)
            }
            if let email = person.email {
                Text(email).font(.caption).textSelection(.enabled)
            }
            if let manager = person.manager {
                Field(label: "Manager", value: manager.name)
            }
            if let topics = person.topics, !topics.isEmpty {
                Field(label: "Knows", value: topics.prefix(8).joined(separator: ", "))
            }
            if let channels = person.channels, !channels.isEmpty {
                Field(label: "Active in", value: channels.prefix(6).map { "#\($0)" }.joined(separator: ", "))
            }
            ForEach(SearchModel.contactLinks(person.links ?? [], deactivated: person.isDeactivated),
                    id: \.self) { link in
                Button(Self.title(link)) { model.openLink(link) }
            }
            if let message = model.message {
                Text(message).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // title names the app a link opens.
    static func title(_ link: String) -> String {
        link.hasPrefix("slack:") ? "Message in Slack" : "Open \(URL(string: link)?.scheme ?? "link")"
    }
}

// Field is one labeled line of a person's detail.
struct Field: View {
    // label names the line.
    let label: String
    // value is the line's text.
    let value: String

    // body shows the label above the value.
    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption).foregroundStyle(.secondary)
            Text(value).font(.callout).fixedSize(horizontal: false, vertical: true)
        }
    }
}
