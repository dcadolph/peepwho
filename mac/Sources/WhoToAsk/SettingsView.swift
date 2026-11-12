import AppKit
import WhoToAskKit
import SwiftUI

// SettingsWindow brings the settings window to the front. The app lives in the menu bar with no
// Dock icon, and macOS leaves a window it opens behind the app in front unless the app activates
// from the person's click and orders the window front itself.
@MainActor
enum SettingsWindow {
    // window is the settings window, recorded when the settings view lands in it.
    static weak var window: NSWindow?

    // open activates the app, opens the settings scene, and raises its window when it is
    // already open, since opening an open scene does not raise it.
    static func open(_ openSettings: OpenSettingsAction) {
        NSApp.activate()
        openSettings()
        raise()
    }

    // raise activates the app and makes the settings window key and front.
    static func raise() {
        guard let window else { return }
        NSApp.activate()
        window.makeKeyAndOrderFront(nil)
    }
}

// WindowReader records the window its view lands in as the settings window and raises it, so a
// settings window opened for the first time comes to the front.
struct WindowReader: NSViewRepresentable {
    // makeNSView returns the view that watches for its window.
    func makeNSView(context: Context) -> NSView { Reader() }

    // updateNSView has nothing to update.
    func updateNSView(_ nsView: NSView, context: Context) {}

    // Reader is an empty view that reports the window it moves into.
    final class Reader: NSView {
        // viewDidMoveToWindow records and raises the window once the view is in one.
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard let window else { return }
            SettingsWindow.window = window
            SettingsWindow.raise()
        }
    }
}

// SettingsView is the settings window: one tab each for general choices, the sources WhoToAsk
// reads, the index, and privacy, above a strip showing the action running and its outcome.
struct SettingsView: View {
    // model holds the settings and runs the actions.
    @ObservedObject var model: SettingsModel
    // confirmingClear shows the Clear Index confirmation.
    @State private var confirmingClear = false
    // confirmingReset shows the Reset WhoToAsk confirmation.
    @State private var confirmingReset = false
    // showingPeople shows the People sheet.
    @State private var showingPeople = false
    // sourceTitle is the title of the card the Sources tab shows, the first card until one is picked.
    @State private var sourceTitle = SourceCard.all[0].title

    // body lays the tabs out above the activity strip every tab shares.
    var body: some View {
        VStack(spacing: 0) {
            TabView {
                ForEach(SettingsTab.allCases) { tab in
                    content(tab).tabItem { Label(tab.title, systemImage: tab.symbol) }
                }
            }
            .padding([.top, .horizontal])
            activity
        }
        .frame(width: 640, height: 600)
        .background(WindowReader())
        .task { await model.loadStatus() }
        .sheet(isPresented: $showingPeople) { PeopleView(model: model) }
        .confirmationDialog("Clear the index?", isPresented: $confirmingClear) {
            Button("Clear Index", role: .destructive) { Task { await model.clearIndex() } }
        } message: {
            Text("This deletes everything WhoToAsk has indexed on this Mac. "
                + "Your tokens and picked folders stay, so Index Now can build it again.")
        }
        .confirmationDialog("Reset WhoToAsk?", isPresented: $confirmingReset) {
            Button("Reset", role: .destructive) { Task { await model.reset() } }
        } message: {
            Text("This deletes the index, every token and site address, and every picked folder and file "
                + "from this Mac. WhoToAsk goes back to the way it was when first installed.")
        }
    }

    // content is the body of tab.
    @ViewBuilder
    private func content(_ tab: SettingsTab) -> some View {
        switch tab {
        case .general: general
        case .sources: sources
        case .index: index
        case .privacy: privacy
        }
    }

    // general holds opening at login, the version, and About.
    private var general: some View {
        Form {
            if model.login != nil || model.loginNote != nil {
                Section {
                    if let login = model.login {
                        Toggle("Open at login", isOn: Binding(
                            get: { login != .off },
                            set: { model.setOpensAtLogin($0) }
                        ))
                    }
                    if let note = model.loginNote {
                        HStack {
                            Text(note).font(.caption).foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                            Spacer()
                            Button("Open System Settings") { SystemLoginItem.openSettings() }.controlSize(.small)
                        }
                    }
                }
            }
            Section {
                HStack {
                    Text(version).foregroundStyle(.secondary)
                    Spacer()
                    Button("About WhoToAsk") { showAbout() }
                }
            }
        }
        .formStyle(.grouped)
    }

    // sources shows a button per source across the top and, under it, the picked card with its
    // fields and whether it is set up.
    private var sources: some View {
        let card = SourceCard.pick(sourceTitle)
        return VStack(spacing: 0) {
            Picker("Source", selection: $sourceTitle) {
                ForEach(SourceCard.all) { Text($0.title).tag($0.title) }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding([.horizontal, .top], 20)
            Form {
                Section {
                    fields(card)
                } header: {
                    HStack {
                        Label(card.title, systemImage: card.symbol)
                        Spacer()
                        badge(card.state(saved: model.saved, placed: Set(model.placeNames.keys), scopes: model.scopes))
                    }
                }
                .id(card.id)
            }
            .formStyle(.grouped)
        }
    }

    // fields are the rows of card: its places, its secrets, then what its source reads.
    @ViewBuilder
    private func fields(_ card: SourceCard) -> some View {
        ForEach(card.slots, id: \.self) { slot in
            PlaceRow(label: placeLabel(slot), slot: slot, model: model)
        }
        ForEach(card.secrets, id: \.self) { name in
            let (label, secure) = secretLabel(name)
            SecretRow(label: label, name: name, model: model, secure: secure)
        }
        switch card.source {
        case "github":
            TextField("Repositories", text: $model.scopes.githubRepos, prompt: Text("owner/name, owner/name"))
            TextField("Or every repository in org", text: $model.scopes.githubOrg)
        case "jira":
            TextField("Projects", text: $model.scopes.jiraProjects, prompt: Text("KEY, KEY"))
            Toggle("Server or Data Center", isOn: $model.scopes.jiraServer)
        case "confluence":
            TextField("Spaces", text: $model.scopes.confluenceSpaces, prompt: Text("KEY, KEY"))
            Toggle("Server or Data Center", isOn: $model.scopes.confluenceServer)
        default:
            EmptyView()
        }
    }

    // badge says whether a card holds what its source needs, and what to add when only the scope
    // is missing.
    private func badge(_ state: CardState) -> some View {
        Group {
            switch state {
            case .setUp:
                Label("Set up", systemImage: "checkmark.circle.fill").foregroundStyle(.green)
            case .needs(let what):
                Label(what, systemImage: "exclamationmark.circle").foregroundStyle(.orange)
            case .notSetUp:
                Text("Not set up").foregroundStyle(.secondary)
            }
        }
        .font(.caption)
    }

    // placeLabel names slot.
    private func placeLabel(_ slot: BookmarkSlot) -> String {
        switch slot {
        case .git: "Git repository"
        case .csv: "Org chart CSV"
        case .codeowners: "CODEOWNERS"
        }
    }

    // secretLabel names the field for secret name and reports whether what is typed stays hidden.
    private func secretLabel(_ name: String) -> (String, Bool) {
        switch name {
        case SecretName.jiraURL, SecretName.confluenceURL: ("Site URL", false)
        case SecretName.jiraEmail, SecretName.confluenceEmail: ("Email", false)
        default: ("Token", true)
        }
    }

    // index shows what the index holds, how fresh each source is, and the actions that build it.
    private var index: some View {
        Form {
            Section {
                headline
                ForEach(model.sourceAges) { row in
                    HStack(alignment: .firstTextBaseline) {
                        Text(row.label)
                        Spacer()
                        Text(row.detail)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.trailing)
                            .lineLimit(3)
                            .textSelection(.enabled)
                    }
                    .font(.caption)
                }
            }
            Section {
                HStack {
                    Group {
                        Button("Index Now") { Task { await model.indexAll() } }
                        Button("Refresh Now") { Task { await model.refresh() } }
                    }
                    .disabled(model.status?.sample == true)
                    Spacer()
                    if model.canLoadSample {
                        Button("Load Sample Company") { Task { await model.loadSample() } }
                    }
                }
                .disabled(model.busy != nil)
                Text(model.refreshNote)
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .formStyle(.grouped)
    }

    // headline is the summary sentence with its people count as a link that opens the People sheet.
    private var headline: some View {
        Text(Self.linked(model.summary, model.peopleWords))
            .fixedSize(horizontal: false, vertical: true)
            .environment(\.openURL, OpenURLAction { url in
                guard url == Self.peopleURL else { return .systemAction }
                showingPeople = true
                return .handled
            })
    }

    // peopleURL is the link the people count carries. It never leaves the window.
    private static let peopleURL = URL(string: "whotoask-settings:people") ?? URL(fileURLWithPath: "/")

    // linked returns summary with the first count matching words made a link to the People sheet.
    private static func linked(_ summary: String, _ words: String?) -> AttributedString {
        var text = AttributedString(summary)
        if let words, let range = text.range(of: words) {
            text[range].link = peopleURL
        }
        return text
    }

    // privacy says how the index is kept and holds the actions that remove what WhoToAsk keeps.
    private var privacy: some View {
        Form {
            Section {
                Label(model.privacy, systemImage: model.status?.sealed == true ? "lock.fill" : "lock.open")
                    .fixedSize(horizontal: false, vertical: true)
            }
            Section {
                HStack {
                    Text("Clear Index deletes everything WhoToAsk has indexed on this Mac. "
                        + "Your tokens and picked folders stay, so Index Now can build it again.")
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer()
                    Button("Clear Index…", role: .destructive) { confirmingClear = true }
                }
                HStack {
                    Text("Reset WhoToAsk deletes the index, every token and site address, "
                        + "and every picked folder and file. Nothing is kept.")
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer()
                    Button("Reset WhoToAsk…", role: .destructive) { confirmingReset = true }
                }
            }
            .disabled(model.busy != nil)
        }
        .formStyle(.grouped)
    }

    // activity shows the action running with its progress and Cancel, then the last outcome.
    @ViewBuilder
    private var activity: some View {
        if model.busy != nil || !model.lines.isEmpty {
            VStack(alignment: .leading, spacing: 4) {
                if let busy = model.busy {
                    HStack {
                        ProgressView().controlSize(.small)
                        Text("\(busy)…")
                        Spacer()
                        if model.canCancel {
                            Button("Cancel") { model.cancel() }.controlSize(.small)
                        }
                    }
                    if let progress = model.progress {
                        Text(progress).font(.caption).foregroundStyle(.secondary)
                            .lineLimit(1).truncationMode(.middle)
                    }
                }
                ForEach(model.lines, id: \.self) { line in
                    Text(line).font(.caption).textSelection(.enabled)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding()
        }
    }

    // version is the version the build stamped into the bundle.
    private var version: String {
        let info = Bundle.main.infoDictionary
        let short = info?["CFBundleShortVersionString"] as? String ?? "unknown"
        let build = info?["CFBundleVersion"] as? String ?? "unknown"
        return "Version \(short) (\(build))"
    }

    // showAbout opens the standard panel, which reads the version and copyright from the bundle.
    // The app has no menu bar of its own to hold it.
    private func showAbout() {
        let credits = NSAttributedString(string: "whotoask.app", attributes: [
            .link: URL(string: "https://whotoask.app") as Any,
            .font: NSFont.systemFont(ofSize: NSFont.smallSystemFontSize),
        ])
        NSApp.activate(ignoringOtherApps: true)
        NSApp.orderFrontStandardAboutPanel(options: [.applicationName: "WhoToAsk", .credits: credits])
    }

}

// PlaceRow shows one picked folder or file with buttons to pick or forget it.
struct PlaceRow: View {
    // label names the place.
    let label: String
    // slot is where the place is kept.
    let slot: BookmarkSlot
    // model keeps and indexes the place.
    @ObservedObject var model: SettingsModel

    // body shows the label, the picked name, and the buttons.
    var body: some View {
        HStack {
            Text(label)
            Spacer()
            Text(model.placeNames[slot] ?? "None").foregroundStyle(.secondary)
            Button("Pick…") { pick() }.disabled(model.busy != nil || model.status?.sample == true)
            if model.placeNames[slot] != nil {
                Button("Forget") { Task { await model.forgetPlace(slot) } }
            }
        }
    }

    // pick shows an open panel and indexes what the user chose.
    private func pick() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = slot == .git
        panel.canChooseFiles = slot != .git
        panel.allowsMultipleSelection = false
        panel.message = slot == .git ? "Pick a git repository folder." : "Pick the \(label) file."
        NSApp.activate(ignoringOtherApps: true)
        guard panel.runModal() == .OK, let url = panel.url else { return }
        Task { await model.pick(url, as: slot) }
    }
}

// SecretRow is one token or site field. A stored value never shows; the field only replaces it.
struct SecretRow: View {
    // label names the field.
    let label: String
    // name is the secret's name in the store.
    let name: String
    // model stores the value.
    @ObservedObject var model: SettingsModel
    // secure hides what is typed.
    var secure = true
    // text is what is typed and not yet saved.
    @State private var text = ""

    // body shows the field with Save and Forget.
    var body: some View {
        HStack {
            Group {
                let prompt = Text(model.has(name) ? "Saved" : "Not set")
                if secure {
                    SecureField(label, text: $text, prompt: prompt)
                } else {
                    TextField(label, text: $text, prompt: prompt)
                }
            }
            .onSubmit(save)
            Button("Save", action: save).disabled(text.isEmpty)
            if model.has(name) {
                Button("Forget") { Task { await model.forgetSecret(name) } }
            }
        }
    }

    // save stores the typed value and clears the field. A value of only spaces forgets the secret
    // the way the Forget button does, so its source stops refreshing too.
    private func save() {
        guard !text.isEmpty else { return }
        if text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            if model.has(name) {
                Task { await model.forgetSecret(name) }
            }
        } else {
            model.saveSecret(name, text)
        }
        text = ""
    }
}
