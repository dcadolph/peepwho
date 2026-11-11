import Foundation
import WhoToAskCore
import Security
import Testing

@testable import WhoToAskKit

// SettingsLayoutTests check the tabs and source cards the settings window is built from, and the
// Privacy tab's line and Reset action through the real core.
@MainActor
struct SettingsLayoutTests {
    // Test 0: The window shows General, Sources, Index, and Privacy in that order, each with a
    // name and a symbol.
    @Test func tabs() {
        #expect(SettingsTab.allCases.map(\.title) == ["General", "Sources", "Index", "Privacy"])
        #expect(Set(SettingsTab.allCases.map(\.symbol)).count == SettingsTab.allCases.count)
        #expect(!SettingsTab.allCases.contains { $0.symbol.isEmpty })
    }

    // Test 1: Every token source and every place has exactly one card, every secret a person types
    // sits on the card of the source that needs it, and the cards show in the agreed order.
    @Test func sourceCards() {
        let cards = SourceCard.all
        #expect(cards.map(\.title) == ["Folders and files", "Slack", "GitHub", "Jira", "Confluence", "PagerDuty"])
        #expect(Set(cards.map(\.id)).count == cards.count)
        var everything = Scopes()
        everything.githubRepos = "o/a"
        everything.jiraProjects = "A"
        everything.confluenceSpaces = "B"
        let tokenSources = IndexJob.forTokens(everything) { _ in true }.map(\.source)
        #expect(cards.map(\.source).filter { !$0.isEmpty } == tokenSources)
        #expect(cards.flatMap(\.slots) == BookmarkSlot.allCases)
        #expect(SourceCard.entered.count == Set(SourceCard.entered).count)
        for card in cards where !card.source.isEmpty {
            #expect(Set(SecretName.required(by: card.source)).isSubset(of: card.secrets), "\(card.title)")
        }
        #expect(!SourceCard.entered.contains(SecretName.indexKey))
        #expect(!SourceCard.entered.contains(SecretName.scopes))
    }

    // Test 2: A token card is set up once every secret its source needs is saved, an optional one
    // does not count, and the places card is set up once any place is picked.
    @Test func connected() throws {
        let jira = try #require(SourceCard.all.first { $0.source == "jira" })
        #expect(!jira.connected(saved: [], placed: []))
        #expect(!jira.connected(saved: [SecretName.jiraToken, SecretName.jiraEmail], placed: [.git]))
        #expect(jira.connected(saved: [SecretName.jiraURL, SecretName.jiraToken], placed: []))
        let slack = try #require(SourceCard.all.first { $0.source == "slack" })
        #expect(!slack.connected(saved: [SecretName.githubToken], placed: [.git]))
        #expect(slack.connected(saved: [SecretName.slackToken], placed: []))
        let places = try #require(SourceCard.all.first { !$0.slots.isEmpty })
        #expect(!places.connected(saved: Set(SourceCard.entered), placed: []))
        for slot in BookmarkSlot.allCases {
            #expect(places.connected(saved: [], placed: [slot]))
        }
    }

    // Test 3: The Privacy line says when the index is not open, and says the index is not
    // encrypted when it opened without a key.
    @Test func privacyLine() async throws {
        let defaults = try #require(UserDefaults(suiteName: "whotoask-test-\(UUID().uuidString)"))
        let closed = SettingsModel(dir: tempDir("whotoask-data"), open: { throw KeychainError(status: errSecInteractionNotAllowed) },
                                   secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await closed.loadStatus()
        #expect(closed.privacy == "The index is not open.")
        let dir = tempDir("whotoask-data")
        defer { try? FileManager.default.removeItem(at: dir) }
        let open = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                 secrets: MemorySecrets(), places: MemoryPlaces(), defaults: defaults)
        await open.loadStatus()
        let sealed = try #require(open.status?.sealed)
        #expect(open.privacy.hasPrefix(sealed ? "The index is encrypted on disk." : "The index is not encrypted on disk."))
    }

    // Test 4: Reset deletes every token, scope, place, the index, and its key, and refuses while a
    // run is going.
    @Test func reset() async throws {
        let repo = try gitRepo()
        let dir = tempDir("whotoask-data")
        defer {
            try? FileManager.default.removeItem(at: repo)
            try? FileManager.default.removeItem(at: dir)
        }
        let defaults = try #require(UserDefaults(suiteName: "whotoask-test-\(UUID().uuidString)"))
        let secrets = MemorySecrets()
        let places = MemoryPlaces()
        let model = SettingsModel(dir: dir, open: { try Core.open(dir: dir, key: "", secrets: KeychainSecrets()) },
                                  secrets: secrets, places: places, defaults: defaults)
        await model.pick(repo, as: .git)
        #expect(model.status?.scopes.count == 1)

        // Tokens are saved only after the run, so no run ever reaches a real service with them.
        async let running: Void = model.indexAll()
        while model.busy == nil { await Task.yield() }
        await model.reset()
        #expect(model.lines.first?.hasSuffix("is still running. Try again when it finishes.") == true)
        #expect(places.has(.git))
        await running

        for name in SourceCard.entered {
            model.saveSecret(name, "value")
        }
        model.scopes.jiraProjects = "KAFKA"
        secrets.values[SecretName.indexKey] = "old key"
        #expect(model.saved == Set(SourceCard.entered))
        await model.reset()
        #expect(model.lines == ["Reset. Nothing is kept."])
        #expect(secrets.values[SecretName.indexKey] == nil)
        #expect(secrets.values[SecretName.scopes] == nil)
        #expect(model.saved.isEmpty)
        #expect(model.placeNames.isEmpty)
        #expect(BookmarkSlot.allCases.allSatisfy { !places.has($0) })
        #expect(SourceCard.entered.allSatisfy { secrets.values[$0] == nil })
        #expect(model.scopes == Scopes())
        #expect(Scopes.load(secrets) == Scopes())
        #expect(model.status?.scopes.isEmpty == true)
        #expect(model.status?.builtAt == nil)
        #expect(model.status?.people == 0)
        #expect(!FileManager.default.fileExists(atPath: dir.path))
        #expect(places.open == 0)
        await model.refresh()
        #expect(model.lines == ["Nothing to refresh yet. Index something first."])
    }

    // Test 5: A button's title picks its card, and a title no card has, or none, picks the first.
    @Test func pick() {
        #expect(SourceCard.pick("Jira").title == "Jira")
        #expect(SourceCard.pick("PagerDuty").title == "PagerDuty")
        #expect(SourceCard.pick("nope").title == "Folders and files")
        #expect(SourceCard.pick(nil).title == "Folders and files")
        for card in SourceCard.all {
            #expect(SourceCard.pick(card.title) == card)
        }
    }
}
