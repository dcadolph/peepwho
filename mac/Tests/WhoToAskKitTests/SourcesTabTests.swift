import AppKit
import SwiftUI
import Testing

@testable import WhoToAsk
@testable import WhoToAskKit

// Segment is one source button as macOS accessibility reports it.
struct Segment {
    // title is the button's label.
    let title: String
    // frame is where the button sits, in window points.
    let frame: NSRect
    // element is the button, which VoiceOver presses through.
    let element: NSAccessibilityProtocol
}

// Badge counts the colored ink in the picked card's header, where its badge sits.
struct Badge {
    // orange is how many points carry the orange of a Needs note.
    let orange: Int
    // green is how many points carry the green of Set up.
    let green: Int
}

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

    // segments are the source buttons as accessibility reports them, left to right.
    func segments() throws -> [Segment] {
        var found: [Segment] = []
        func walk(_ element: NSAccessibilityProtocol) {
            if element.accessibilityRole() == .radioButton {
                let title = element.accessibilityTitle() ?? ""
                found.append(Segment(
                    title: title.isEmpty ? element.accessibilityLabel() ?? "" : title,
                    frame: hosted.window.convertFromScreen(element.accessibilityFrame()),
                    element: element
                ))
            }
            for child in element.accessibilityChildren() ?? [] {
                if let child = child as? NSAccessibilityProtocol { walk(child) }
            }
        }
        walk(try control())
        return found.sorted { $0.frame.minX < $1.frame.minX }
    }

    // pick presses the button for title the way VoiceOver does and waits for its card. The press
    // reports false for a segment even when it selects it, so the selection is what is checked.
    func pick(_ title: String) throws {
        let found = try segments().first { $0.title.hasPrefix(title) }
        let segment = try #require(found, "no button \(title)")
        _ = segment.element.accessibilityPerformPress()
        settle(0.2)
        #expect(try labels()[try control().selectedSegment].hasPrefix(title))
    }

    // badge counts the orange and green ink in the header under the buttons, where the picked
    // card's badge sits.
    func badge() throws -> Badge {
        let control = try control()
        let rep = try hosted.render()
        let below = hosted.view.convert(control.bounds, from: control).maxY
        var orange = 0
        var green = 0
        for y in stride(from: below + 8, to: below + 48, by: 1) {
            for x in stride(from: rep.size.width - 240, to: rep.size.width - 8, by: 1) {
                let scale = CGFloat(rep.pixelsWide) / rep.size.width
                guard let color = rep.colorAt(x: Int(x * scale), y: Int(y * scale))?
                    .usingColorSpace(.sRGB), color.alphaComponent > 0.5 else { continue }
                let (r, g, b) = (color.redComponent, color.greenComponent, color.blueComponent)
                if r > 0.85, g > 0.35, g < 0.75, b < 0.3 { orange += 1 }
                if g > 0.6, r < 0.45, b < 0.5 { green += 1 }
            }
        }
        return Badge(orange: orange, green: green)
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
        // Test 0: Every source button sits inside the window at its own width with room for its
        // whole title, Folders and files first, and nothing reaches the screen.
        @Test func buttonsShowWholeTitles() throws {
            let tab = try SourcesTab.open(MemorySecrets(), MemoryPlaces())
            defer { tab.close() }
            let titles = SourceCard.all.map(\.title)
            #expect(titles.first == "Folders and files")
            let segments = try tab.segments()
            #expect(segments.map(\.title) == titles)
            let font = try tab.control().font ?? .systemFont(ofSize: NSFont.systemFontSize)
            let inside = tab.hosted.view.bounds
            for segment in segments {
                let title = NSAttributedString(string: segment.title, attributes: [.font: font])
                let room = segment.frame.width - title.size().width
                #expect(room >= 16, "\(segment.title) has \(room) points to spare")
                #expect(segment.frame.minX >= inside.minX && segment.frame.maxX <= inside.maxX,
                        "\(segment.title) at \(segment.frame) leaves the window")
            }
            #expect(tab.hosted.window.fenced == ["makeKeyAndOrderFront"])
            #expect(!tab.hosted.window.isVisible)
            #expect(!NSApp.isActive)
        }

        // Test 1: Set-up sources carry a check mark and a green badge, and GitHub, Jira, and
        // Confluence with their secrets and nothing to read carry none and an orange Needs note
        // until it is added.
        @Test func checkMarksAndNeeds() throws {
            let secrets = MemorySecrets()
            for name in SourceCard.entered where name != SecretName.pagerdutyToken {
                secrets.values[name] = "value"
            }
            let places = MemoryPlaces()
            places.urls[.csv] = URL(fileURLWithPath: "/nowhere/org.csv")
            let tab = try SourcesTab.open(secrets, places)
            defer { tab.close() }
            #expect(try tab.segments().map(\.title) == [
                "Folders and files ✓", "Slack ✓", "GitHub", "Jira", "Confluence", "PagerDuty",
            ])
            let wants: [(String, CardState)] = [
                ("Folders and files", .setUp), ("Slack", .setUp),
                ("GitHub", .needs("Needs a repository")), ("Jira", .needs("Needs a project")),
                ("Confluence", .needs("Needs a space")), ("PagerDuty", .notSetUp),
            ]
            for (title, want) in wants {
                try tab.pick(title)
                let card = SourceCard.pick(title)
                let state = card.state(
                    saved: tab.model.saved, placed: Set(tab.model.placeNames.keys),
                    scopes: tab.model.scopes
                )
                #expect(state == want, "\(title)")
                let badge = try tab.badge()
                switch want {
                case .setUp: #expect(badge.green > 10 && badge.orange == 0, "\(title) \(badge)")
                case .needs: #expect(badge.orange > 10 && badge.green == 0, "\(title) \(badge)")
                case .notSetUp: #expect(badge.orange == 0 && badge.green == 0, "\(title) \(badge)")
                }
            }
            tab.model.scopes.githubRepos = "acme/billing"
            tab.model.scopes.jiraProjects = "PAY"
            tab.model.scopes.confluenceSpaces = "ENG"
            settle(0.2)
            #expect(try tab.segments().map(\.title) == [
                "Folders and files ✓", "Slack ✓", "GitHub ✓", "Jira ✓", "Confluence ✓", "PagerDuty",
            ])
            try tab.pick("Jira")
            let badge = try tab.badge()
            #expect(badge.green > 10 && badge.orange == 0, "Jira \(badge)")
        }

        // Test 2: A site address typed and not saved is still there after switching to another
        // source and back, and a typed token is not.
        @Test func typedSiteAddressStays() throws {
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
            #expect(tab.model.saved.isEmpty, "nothing is saved until Save")
        }
    }
}
