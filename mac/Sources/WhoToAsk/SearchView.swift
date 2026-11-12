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
    // height is the content's height, which the window is sized to.
    @State private var height: CGFloat = 0
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
                .onKeyPress(.downArrow) {
                    announce(model.moveDown())
                    return .handled
                }
                .onKeyPress(.upArrow) {
                    announce(model.moveUp())
                    return .handled
                }
                .onKeyPress(.escape) { model.escape() ? .handled : .ignored }
                .onKeyPress(keys: [.return]) { press in
                    guard press.modifiers.contains(.command) else { return .ignored }
                    Task { await model.showMarked() }
                    return .handled
                }
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
                if model.empty, model.query.isEmpty {
                    Text(SearchModel.noIndex).font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                ResultsView(model: model)
            }
            Divider()
            HStack {
                if let hint = model.enterHint {
                    Text(hint).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Button("Settings…") { SettingsWindow.open(openSettings) }.controlSize(.small)
                Button("Quit") { NSApp.terminate(nil) }.controlSize(.small)
            }
        }
        .padding(12)
        .fixedSize(horizontal: false, vertical: true)
        .frame(width: 360, alignment: .top)
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
        .background(WindowFitter(height: height))
        .onAppear {
            focused = true
            Task {
                await model.opened()
                await onOpen()
            }
        }
        .onDisappear { model.closed() }
    }

    // announce says the name of the person the arrow keys just marked to VoiceOver, since focus
    // stays in the field and the mark alone is never spoken.
    private func announce(_ person: AskPerson?) {
        guard let person else { return }
        AccessibilityNotification.Announcement(person.name).post()
    }
}

// WindowFitter sizes the window it sits in to height whenever height changes.
struct WindowFitter: NSViewRepresentable {
    // height is the content height the window should have.
    let height: CGFloat

    // makeNSView returns an empty view, used only to reach its window.
    func makeNSView(context: Context) -> NSView { NSView() }

    // updateNSView resizes the window after the current layout pass finishes.
    func updateNSView(_ view: NSView, context: Context) {
        let height = height
        DispatchQueue.main.async { WindowFit.apply(view.window, height: height) }
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
            ForEach(Array(model.people.enumerated()), id: \.element.id) { index, person in
                Button { Task { await model.select(person.id) } } label: {
                    PersonRow(person: person)
                }
                .buttonStyle(.plain)
                // The marked row gets a quiet background drawn past its edges, so marking it
                // changes nothing about its size.
                .background {
                    MarkBackground(marked: model.selected == index).padding(.horizontal, -4)
                }
                .accessibilityAddTraits(model.selected == index ? .isSelected : [])
            }
            if !model.closest.isEmpty {
                Text("Closest by name").font(.caption).foregroundStyle(.secondary)
                ForEach(model.closest) { hit in
                    Button { Task { await model.select(hit.id) } } label: {
                        ClosestRow(hit: hit)
                    }
                    .buttonStyle(.plain)
                }
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

// MarkBackground is the shape behind a result row, filled when the row is marked. With Increase
// Contrast on, the fill is stronger and outlined, since a faint fill alone is hard to see.
struct MarkBackground: View {
    // marked reports whether the row is the one Enter acts on.
    let marked: Bool
    // contrast is the system contrast setting.
    @Environment(\.colorSchemeContrast) private var contrast

    // body fills the row's shape when marked and draws nothing otherwise.
    var body: some View {
        let increased = contrast == .increased
        RoundedRectangle(cornerRadius: 5)
            .fill(marked ? Color.primary.opacity(increased ? 0.2 : 0.08) : .clear)
            .overlay {
                if marked, increased {
                    RoundedRectangle(cornerRadius: 5).strokeBorder(Color.primary.opacity(0.6))
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
                NameText(name: person.name, deactivated: person.isDeactivated).font(.body.weight(.medium))
                Spacer()
                StrengthBar(strength: person.strength)
            }
            if !person.role.isEmpty {
                RoleText(line: person.role, deactivated: person.isDeactivated).font(.caption)
            }
            if let reasons = person.reasons?.joined(separator: ", ").nonEmpty {
                Text(reasons).font(.caption2).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}

// ClosestRow is one person the name search found when the answer named no one: name and role,
// with the Deactivated mark for a person who can no longer be reached.
struct ClosestRow: View {
    // hit is the person found.
    let hit: SearchResult

    // body shows the name with the role line under it.
    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            NameText(name: hit.name, deactivated: hit.isDeactivated).font(.body.weight(.medium))
            if !hit.role.isEmpty {
                RoleText(line: hit.role, deactivated: hit.isDeactivated).font(.caption)
            }
        }
        .padding(.vertical, 3)
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }
}

// NameText is a person's name, crossed out and dimmed when they can no longer be reached.
struct NameText: View {
    // name is the person's name.
    let name: String
    // deactivated reports whether the person can no longer be reached.
    let deactivated: Bool

    // body draws the name, struck through in gray for a deactivated person.
    var body: some View {
        Text(name).strikethrough(deactivated).foregroundStyle(deactivated ? .secondary : .primary)
    }
}

// RoleText is the line under a person's name, gray, with the Deactivated mark in orange so a
// person who can no longer be reached stands out.
struct RoleText: View {
    // line is the person's role line, led by the mark when deactivated.
    let line: String
    // deactivated reports whether the person can no longer be reached.
    let deactivated: Bool

    // body draws the mark in semibold orange and the rest of the line in gray.
    var body: some View {
        let parts = Role.split(line, deactivated: deactivated)
        let rest = Text(parts.rest).foregroundStyle(.secondary)
        if let mark = parts.mark {
            Text(mark).fontWeight(.semibold).foregroundStyle(.orange) + rest
        } else {
            rest
        }
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
        .accessibilityElement()
        .accessibilityLabel("Match strength")
        .accessibilityValue(SearchModel.strengthValue(strength))
    }
}

extension String {
    // nonEmpty is the string, or nil when it is empty.
    var nonEmpty: String? { isEmpty ? nil : self }
}
