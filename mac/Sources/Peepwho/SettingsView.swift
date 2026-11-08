import AppKit
import PeepwhoKit
import SwiftUI

// SettingsView is the settings window: what the index holds, the places and tokens it reads,
// and the actions that build, refresh, and wipe it.
struct SettingsView: View {
    // model holds the settings and runs the actions.
    @ObservedObject var model: SettingsModel
    // confirmingWipe shows the wipe confirmation.
    @State private var confirmingWipe = false

    // body lays out the index summary above the sources.
    var body: some View {
        Form {
            Section("Index") {
                Text(model.summary).fixedSize(horizontal: false, vertical: true)
                if let counts = sourceCounts {
                    Text(counts).font(.caption).foregroundStyle(.secondary)
                }
                ForEach(model.sourceAges) { row in
                    HStack(alignment: .firstTextBaseline) {
                        Text(row.source)
                        Spacer()
                        Text(row.detail)
                            .foregroundStyle(.secondary)
                            .multilineTextAlignment(.trailing)
                            .lineLimit(3)
                            .textSelection(.enabled)
                    }
                    .font(.caption)
                }
                HStack {
                    Group {
                        Button("Index Now") { Task { await model.indexAll() } }
                        Button("Refresh Now") { Task { await model.refresh() } }
                    }
                    .disabled(model.status?.sample == true)
                    if model.canLoadSample {
                        Button("Load Sample Company") { Task { await model.loadSample() } }
                    }
                    Spacer()
                    Button("Wipe…", role: .destructive) { confirmingWipe = true }
                }
                .disabled(model.busy != nil)
                if let busy = model.busy {
                    HStack { ProgressView().controlSize(.small); Text("\(busy)…") }
                }
                ForEach(model.lines, id: \.self) { line in
                    Text(line).font(.caption).textSelection(.enabled)
                }
            }
            Section("Folders and files") {
                PlaceRow(label: "Git repository", slot: .git, model: model)
                PlaceRow(label: "Org chart CSV", slot: .csv, model: model)
                PlaceRow(label: "CODEOWNERS", slot: .codeowners, model: model)
            }
            Section("Slack") {
                SecretRow(label: "Token", name: SecretName.slackToken, model: model)
            }
            Section("GitHub") {
                SecretRow(label: "Token", name: SecretName.githubToken, model: model)
                TextField("Repositories", text: $model.scopes.githubRepos, prompt: Text("owner/name, owner/name"))
                TextField("Or every repository in org", text: $model.scopes.githubOrg)
            }
            Section("Jira") {
                SecretRow(label: "Site URL", name: SecretName.jiraURL, model: model, secure: false)
                SecretRow(label: "Email", name: SecretName.jiraEmail, model: model, secure: false)
                SecretRow(label: "Token", name: SecretName.jiraToken, model: model)
                TextField("Projects", text: $model.scopes.jiraProjects, prompt: Text("KEY, KEY"))
                Toggle("Server or Data Center", isOn: $model.scopes.jiraServer)
            }
            Section("Confluence") {
                SecretRow(label: "Site URL", name: SecretName.confluenceURL, model: model, secure: false)
                SecretRow(label: "Email", name: SecretName.confluenceEmail, model: model, secure: false)
                SecretRow(label: "Token", name: SecretName.confluenceToken, model: model)
                TextField("Spaces", text: $model.scopes.confluenceSpaces, prompt: Text("KEY, KEY"))
                Toggle("Server or Data Center", isOn: $model.scopes.confluenceServer)
            }
            Section("PagerDuty") {
                SecretRow(label: "Token", name: SecretName.pagerdutyToken, model: model)
            }
        }
        .formStyle(.grouped)
        .frame(width: 520, height: 640)
        .task { await model.loadStatus() }
        .confirmationDialog("Wipe the index?", isPresented: $confirmingWipe) {
            Button("Wipe", role: .destructive) { Task { await model.wipe() } }
        } message: {
            Text("This deletes everything peepwho has indexed on this Mac. Tokens and picked folders stay.")
        }
    }

    // sourceCounts lists the records each source holds, nil when there are none.
    private var sourceCounts: String? {
        guard let sources = model.status?.sources, !sources.isEmpty else { return nil }
        return sources.keys.sorted().map { "\($0) \(sources[$0] ?? 0)" }.joined(separator: ", ")
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
                Button("Forget") { model.forgetPlace(slot) }
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
                Button("Forget") { model.forgetSecret(name) }
            }
        }
    }

    // save stores the typed value and clears the field.
    private func save() {
        guard !text.isEmpty else { return }
        model.saveSecret(name, text)
        text = ""
    }
}
