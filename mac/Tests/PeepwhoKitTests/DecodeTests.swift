import Foundation
import Testing

@testable import PeepwhoKit

// fixture reads one core answer the Go test TestSwiftFixtures wrote under core/testdata/swift.
func fixture(_ name: String) throws -> String {
    let url = repoRoot.appendingPathComponent("core/testdata/swift/\(name).json")
    return try String(contentsOf: url, encoding: .utf8)
}

// DecodeTests decode every core answer the app reads, so a field the core renames fails here.
struct DecodeTests {
    // ask decodes people and channels with their scores.
    @Test func ask() throws {
        let got: AskAnswer = try Core.decode(fixture("ask"))
        #expect(got.query == "terraform")
        #expect(got.people?.first?.name == "Carol Lee")
        #expect(got.people?.first?.strength == 1)
        #expect(got.channels?.first?.members?.isEmpty == false)
    }

    // search decodes matches with their kind.
    @Test func search() throws {
        let got: SearchAnswer = try Core.decode(fixture("search"))
        #expect(got.results?.first?.id == "angela@corp.com")
        #expect(got.results?.first?.kind.isEmpty == false)
    }

    // person decodes the manager and the Slack link.
    @Test func person() throws {
        let got: PersonDetail = try Core.decode(fixture("person"))
        #expect(got.name == "Angela Malone")
        #expect(got.manager?.name.isEmpty == false)
        #expect(got.links?.first?.hasPrefix("slack://user?") == true)
    }

    // recall decodes episodes and the store's scope.
    @Test func recall() throws {
        let got: RecallAnswer = try Core.decode(fixture("recall"))
        #expect(got.person == "angela@corp.com")
        #expect(got.episodes?.isEmpty == false)
        #expect(got.scope.episodes > 0)
    }

    // status decodes the counts, the seal, and the age.
    @Test func status() throws {
        let got: IndexStatus = try Core.decode(fixture("status"))
        #expect(got.people > 0)
        #expect(got.builtAt?.isEmpty == false)
        #expect((3599...3600).contains(got.ageSeconds))
        #expect(got.sources["git"] != nil)
        let git = try #require(got.scopes.first)
        #expect(git.source == "git")
        #expect(git.scope == "repo:/src/app")
        #expect(git.ranAt?.isEmpty == false)
        #expect((3599...3600).contains(git.ageSeconds))
        #expect(git.attemptedAt?.isEmpty == false)
        #expect(git.error == "repository not found")
        let slack = try #require(got.scopes.last)
        #expect(slack.source == "slack")
        #expect(slack.ranAt == nil)
        #expect(slack.attemptedAt == nil)
        #expect(slack.error == nil)
    }

    // changes decodes the merged changes and when they ran.
    @Test func changes() throws {
        let got: ChangesAnswer = try Core.decode(fixture("changes"))
        #expect(got.entries == 2)
        #expect(got.asOf?.isEmpty == false)
        #expect(got.changes.peopleJoined == ["Ann Lee"])
        #expect(got.changes.teamsRemoved == ["Payments"])
        #expect(!got.changes.isEmpty)
    }

    // index decodes the source and scope a run read.
    @Test func index() throws {
        let got: IndexResult = try Core.decode(fixture("index"))
        #expect(got.source == "git")
        #expect(got.scope.hasPrefix("repo:"))
    }

    // refresh decodes each scope's outcome.
    @Test func refresh() throws {
        let got: RefreshResult = try Core.decode(fixture("refresh"))
        #expect(got.ok)
        #expect(got.results.first?.ranAt.isEmpty == false)
    }

    // wipe decodes the files removed.
    @Test func wipe() throws {
        let got: WipeResult = try Core.decode(fixture("wipe"))
        #expect(got.removed?.isEmpty == false)
    }
}
