import AppKit
import SwiftUI
import Testing

@testable import WhoToAsk
@testable import WhoToAskKit

// SourcesTab is the real settings window hosted on its Sources tab.
@MainActor
struct SourcesTab {
    // hosted is the settings window in its fence.
    let hosted: Hosted<SettingsView>
    // model is the settings the window shows.
    let model: SettingsModel
    // dir is the data directory, removed by close.
    let dir: URL

    // open hosts the settings window on its Sources tab at its own size, over secrets and places.
    // A hosted window has no toolbar to hold the tab buttons, so the tab is picked as it opens.
    static func open(_ secrets: MemorySecrets, _ places: MemoryPlaces) throws -> SourcesTab {
        let dir = tempDir("whotoask-view")
        let defaults = try #require(UserDefaults(suiteName: "whotoask-view-\(UUID().uuidString)"))
        let core = CoreMemorySecrets(secrets)
        let model = SettingsModel(
            dir: dir, open: { try Core.open(dir: dir, key: "", secrets: core) },
            secrets: secrets, places: places, defaults: defaults
        )
        let view = SettingsView(model: model, tab: .sources)
        let hosted = host(view, size: NSSize(width: 640, height: 600))
        return SourcesTab(hosted: hosted, model: model, dir: dir)
    }

    // control is the row of source buttons.
    func control() throws -> NSSegmentedControl {
        try #require(views(hosted.view, NSSegmentedControl.self).first, "no source buttons")
    }

    // labels are the source buttons' labels, left to right.
    func labels() throws -> [String] {
        let control = try control()
        return (0..<control.segmentCount).map { control.label(forSegment: $0) ?? "" }
    }

    // pick presses the button for title the way a click does, through the control's action.
    func pick(_ title: String) throws {
        let control = try control()
        let found = try labels().firstIndex { $0.hasPrefix(title) }
        let index = try #require(found, "no button \(title)")
        control.selectedSegment = index
        control.sendAction(control.action, to: control.target)
        settle(0.2)
    }

    // fields are the editable text fields the picked card shows, top to bottom.
    func fields() -> [NSTextField] {
        views(hosted.view, NSTextField.self)
            .filter(\.isEditable)
            .sorted { $0.convert($0.bounds, to: nil).maxY > $1.convert($1.bounds, to: nil).maxY }
    }

    // close takes the window down and removes the data directory.
    func close() {
        hosted.window.close()
        try? FileManager.default.removeItem(at: dir)
    }
}

// views returns every view of type under root, root included, in depth-first order.
@MainActor
func views<T: NSView>(_ root: NSView, _ type: T.Type) -> [T] {
    var found: [T] = []
    func walk(_ view: NSView) {
        if let match = view as? T { found.append(match) }
        view.subviews.forEach(walk)
    }
    walk(root)
    return found
}

extension ViewTests {
    // SourcesTabTests check the Sources tab in the real settings window: the buttons, their check
    // marks, the Needs notes, and typed site addresses.
    @MainActor
    @Suite
    struct SourcesTabTests {
        // Test 0: Every source button reads in full at the window's own width, Folders and files
        // first, and nothing reaches the screen.
        @Test func buttonsReadInFull() async throws {
            let tab = try SourcesTab.open(MemorySecrets(), MemoryPlaces())
            defer { tab.close() }
            let titles = SourceCard.all.map(\.title)
            #expect(titles.first == "Folders and files")
            #expect(try tab.labels() == titles)
            let lines = try await tab.hosted.read(until: "PagerDuty").map(\.text)
            for title in titles {
                let found = lines.contains { $0.contains(title) }
                #expect(found, "\(title) not read in full: \(lines)")
            }
            #expect(tab.hosted.window.fenced == ["makeKeyAndOrderFront"])
            #expect(!tab.hosted.window.isVisible)
            #expect(!NSApp.isActive)
        }

        // Test 1: Set-up sources carry a check mark, and GitHub, Jira, and Confluence with their
        // secrets and nothing to read carry none and show what they need until it is added.
        @Test func checkMarksAndNeeds() async throws {
            let secrets = MemorySecrets()
            for name in SourceCard.entered where name != SecretName.pagerdutyToken {
                secrets.values[name] = "value"
            }
            let places = MemoryPlaces()
            places.urls[.csv] = URL(fileURLWithPath: "/nowhere/org.csv")
            let tab = try SourcesTab.open(secrets, places)
            defer { tab.close() }
            #expect(try tab.labels() == [
                "Folders and files ✓", "Slack ✓", "GitHub", "Jira", "Confluence", "PagerDuty",
            ])
            let wants = [
                ("Folders and files", "Set up"), ("Slack", "Set up"),
                ("GitHub", "Needs a repository"), ("Jira", "Needs a project"),
                ("Confluence", "Needs a space"), ("PagerDuty", "Not set up"),
            ]
            for (title, badge) in wants {
                try tab.pick(title)
                let lines = try await tab.hosted.read(until: badge).map(\.text)
                #expect(lines.contains { $0.contains(badge) }, "\(title) badge: \(lines)")
            }
            tab.model.scopes.githubRepos = "acme/billing"
            tab.model.scopes.jiraProjects = "PAY"
            tab.model.scopes.confluenceSpaces = "ENG"
            settle(0.2)
            #expect(try tab.labels() == [
                "Folders and files ✓", "Slack ✓", "GitHub ✓", "Jira ✓", "Confluence ✓", "PagerDuty",
            ])
            try tab.pick("Jira")
            let lines = try await tab.hosted.read(until: "Set up").map(\.text)
            #expect(lines.contains { $0.contains("Set up") }, "Jira badge: \(lines)")
            #expect(!lines.contains { $0.contains("Needs") }, "Jira still needs: \(lines)")
        }

        // Test 2: A site address typed and not saved is still there after switching to another
        // source and back, and a typed token is not.
        @Test func typedSiteAddressStays() async throws {
            let tab = try SourcesTab.open(MemorySecrets(), MemoryPlaces())
            defer { tab.close() }
            #expect(SourceCard.pick("Jira").secrets.first == SecretName.jiraURL)
            try tab.pick("Jira")
            let site = try #require(tab.fields().first { !($0 is NSSecureTextField) })
            #expect(tab.hosted.window.makeFirstResponder(site))
            tab.hosted.type("acme.atlassian.net")
            let token = try #require(tab.fields().first { $0 is NSSecureTextField })
            #expect(tab.hosted.window.makeFirstResponder(token))
            tab.hosted.type("secret")
            try tab.pick("Slack")
            try tab.pick("Jira")
            let again = try #require(tab.fields().first { !($0 is NSSecureTextField) })
            #expect(again.stringValue == "acme.atlassian.net")
            #expect(tab.fields().first { $0 is NSSecureTextField }?.stringValue == "")
            let lines = try await tab.hosted.read(until: "acme").map(\.text)
            #expect(lines.contains { $0.contains("acme.atlassian.net") }, "read \(lines)")
            #expect(tab.model.saved.isEmpty, "nothing is saved until Save")
        }
    }
}
