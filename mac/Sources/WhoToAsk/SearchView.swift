import AppKit
import WhoToAskKit
import SwiftUI

// SearchView is the menu bar window: the search field, then the results or one person.
struct SearchView: View {
    // model answers the field and holds what the window shows.
    @ObservedObject var model: SearchModel
    // onOpen runs each time the window opens, after the changes are read.
    var onOpen: () async -> Void = {}
    // focused keeps the cursor in the field whenever the window opens.
    @FocusState private var focused: Bool
    // openSettings opens the settings scene.
    @Environment(\.openSettings) private var openSettings

    // body lays out the field above the results or the person.
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            TextField("Who knows about…", text: $model.query)
                .textFieldStyle(.roundedBorder)
                .focused($focused)
                .onSubmit { Task { await model.submit() } }
                .onChange(of: model.query) { _, text in model.typed(text) }
            if model.sample {
                Text("Sample company").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
            }
            if let failure = model.refreshFailure {
                Label(failure, systemImage: "exclamationmark.triangle")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if !model.changes.isEmpty, model.detail == nil {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Changed since you last looked").font(.caption.weight(.semibold))
                    ForEach(model.changes, id: \.self) { line in
                        Text(line).font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
            if let person = model.detail {
                PersonView(person: person, model: model)
            } else {
                ResultsView(model: model)
            }
            Divider()
            HStack {
                Text("Enter messages the first person.").font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button("Settings…") { SettingsWindow.open(openSettings) }.controlSize(.small)
                Button("Quit") { NSApp.terminate(nil) }.controlSize(.small)
            }
        }
        .padding(12)
        .frame(width: 360)
        .onAppear {
            focused = true
            Task {
                await model.opened()
                await onOpen()
            }
        }
        .onDisappear { model.closed() }
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
            if !model.teams.isEmpty {
                Text("Teams of record").font(.caption).foregroundStyle(.secondary).padding(.top, 4)
                ForEach(model.teams) { team in
                    TeamRow(team: team)
                }
            }
        }
    }
}

// TeamRow is one team of record: its handle and the org chart team it names. It is plain text
// on purpose, since a team has no profile to show and nobody to message.
struct TeamRow: View {
    // team is the team shown.
    let team: AskTeam

    // body shows the handle, with the org chart team beside it when one matches.
    var body: some View {
        HStack {
            Text(team.name).font(.callout)
            if let label = team.team?.nonEmpty {
                Text(label).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
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
        if let url = channel.url, SearchModel.openable(url) != nil {
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
