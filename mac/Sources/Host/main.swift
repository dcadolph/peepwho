import AppKit
import SwiftUI

// ContentView is the host window: one button per proof and the log pane.
struct ContentView: View {
    // model runs the core calls and holds the log.
    @ObservedObject var model: HostModel
    // jiraURL is the Jira site typed into the form.
    @State private var jiraURL = ""
    // jiraEmail is the Jira account email typed into the form.
    @State private var jiraEmail = ""
    // jiraToken is the Jira API token typed into the form.
    @State private var jiraToken = ""
    // jiraProject is the Jira project key to index once.
    @State private var jiraProject = ""
    // jiraServer selects the Server or Data Center API over Cloud.
    @State private var jiraServer = false

    // body lays out the buttons above the log.
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Button("Pick git folder") { model.pick(.git) }
                Button("Pick CSV file") { model.pick(.csv) }
                Button("Refresh") { model.refresh() }
                Button("Wipe") { model.wipe() }
            }
            HStack {
                TextField("Jira URL", text: $jiraURL)
                TextField("Email", text: $jiraEmail)
                SecureField("Token", text: $jiraToken)
                TextField("Project", text: $jiraProject).frame(width: 80)
                Toggle("Server", isOn: $jiraServer)
                Button("Save Jira") {
                    model.saveJira(
                        url: jiraURL, email: jiraEmail, token: jiraToken,
                        project: jiraProject, server: jiraServer
                    )
                    jiraToken = ""
                }
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(model.lines.enumerated()), id: \.offset) { _, line in
                        Text(line).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(12)
        .frame(minWidth: 720, minHeight: 420)
    }
}

// AppDelegate builds the window, runs the launch sequence, and adopts files opened with the app.
final class AppDelegate: NSObject, NSApplicationDelegate {
    // model is the one host model the window and the arguments drive.
    let model = HostModel()
    // window is the host's only window.
    private var window: NSWindow?

    // applicationDidFinishLaunching shows the window, runs the launch sequence, then the
    // scripted steps the arguments name.
    func applicationDidFinishLaunching(_ notification: Notification) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 760, height: 480),
            styleMask: [.titled, .closable, .resizable, .miniaturizable],
            backing: .buffered, defer: false
        )
        window.title = "peepwho host"
        window.contentView = NSHostingView(rootView: ContentView(model: model))
        window.center()
        window.makeKeyAndOrderFront(nil)
        self.window = window
        model.launch()
        runArguments(Array(CommandLine.arguments.dropFirst()))
    }

    // application adopts a folder opened with the app as the git place and a file as the CSV,
    // which grants the same sandbox access an open panel does.
    func application(_ application: NSApplication, open urls: [URL]) {
        for url in urls {
            let isDir = (try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) ?? false
            model.adopt(url, as: isDir ? .git : .csv)
        }
    }

    // applicationShouldTerminateAfterLastWindowClosed quits when the window closes.
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
    }

    // runArguments queues the scripted steps after the launch sequence, in the order given:
    // --save-jira URL EMAIL TOKEN PROJECT server|cloud, --refresh, --forget-token, --wipe, and
    // --quit. They exist so the checks in docs/APPS.md run without clicks.
    private func runArguments(_ args: [String]) {
        var i = 0
        while i < args.count {
            switch args[i] {
            case "--save-jira" where i + 5 < args.count:
                model.saveJira(
                    url: args[i + 1], email: args[i + 2], token: args[i + 3],
                    project: args[i + 4], server: args[i + 5] == "server"
                )
                i += 5
            case "--refresh":
                model.refresh()
            case "--forget-token":
                model.forgetToken()
            case "--wipe":
                model.wipe()
            case "--quit":
                model.run {
                    self.model.say("quit")
                    DispatchQueue.main.async { NSApp.terminate(nil) }
                }
            default:
                break
            }
            i += 1
        }
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
