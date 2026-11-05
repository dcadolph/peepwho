import AppKit
import PeepwhoKit
import SwiftUI

// SearchView is the menu bar window: the search field, then the results or one person.
struct SearchView: View {
    // model answers the field and holds what the window shows.
    @ObservedObject var model: SearchModel
    // focused keeps the cursor in the field whenever the window opens.
    @FocusState private var focused: Bool

    // body lays out the field above the results or the person.
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            TextField("Who knows about…", text: $model.query)
                .textFieldStyle(.roundedBorder)
                .focused($focused)
                .onSubmit { Task { await model.submit() } }
                .onChange(of: model.query) { _, text in model.typed(text) }
            if let person = model.detail {
                PersonView(person: person, model: model)
            } else {
                ResultsView(model: model)
            }
            Divider()
            HStack {
                Text("Enter messages the first person.").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Quit") { NSApp.terminate(nil) }.controlSize(.small)
            }
        }
        .padding(12)
        .frame(width: 360)
        .onAppear { focused = true }
    }
}

// ResultsView lists the people and channels the last question suggests.
struct ResultsView: View {
    // model holds the answer and shows a person on click.
    @ObservedObject var model: SearchModel

    // body lists people first, then channels, or says why there is nothing.
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let message = model.message {
                Text(message).font(.callout).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            ForEach(model.people) { person in
                Button { Task { await model.select(person.id) } } label: {
                    PersonRow(person: person)
                }
                .buttonStyle(.plain)
            }
            if !model.channels.isEmpty {
                Text("Ask in").font(.caption).foregroundStyle(.secondary).padding(.top, 4)
                ForEach(model.channels, id: \.name) { channel in
                    ChannelRow(channel: channel, model: model)
                }
            }
        }
    }
}

// PersonRow is one suggested person: name, role, and why they matched.
struct PersonRow: View {
    // person is the suggestion shown.
    let person: AskPerson

    // body shows the name and role on one line and the reasons below.
    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack {
                Text(person.name).font(.body.weight(.medium))
                Spacer()
                StrengthBar(strength: person.strength)
            }
            if let role = [person.title, person.team].compactMap({ $0 }).joined(separator: ", ").nonEmpty {
                Text(role).font(.caption).foregroundStyle(.secondary)
            }
            if let reasons = person.reasons?.joined(separator: ", ").nonEmpty {
                Text(reasons).font(.caption2).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}

// ChannelRow is one suggested channel, which opens when it has a link.
struct ChannelRow: View {
    // channel is the suggestion shown.
    let channel: AskChannel
    // model opens the channel's link.
    let model: SearchModel

    // body shows the channel name, as a link when it has one.
    var body: some View {
        if let url = channel.url {
            Button("#\(channel.name)") { model.openLink(url) }.buttonStyle(.link)
        } else {
            Text("#\(channel.name)")
        }
    }
}

// StrengthBar draws a match's strength relative to the best one.
struct StrengthBar: View {
    // strength is zero to one.
    let strength: Double

    // body is a short filled bar.
    var body: some View {
        ZStack(alignment: .leading) {
            Capsule().fill(.quaternary)
            Capsule().fill(.tint).frame(width: 40 * max(0.05, min(1, strength)))
        }
        .frame(width: 40, height: 4)
    }
}

extension String {
    // nonEmpty is the string, or nil when it is empty.
    var nonEmpty: String? { isEmpty ? nil : self }
}
