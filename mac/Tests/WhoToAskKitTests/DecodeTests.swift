import Foundation
import Testing

@testable import WhoToAskKit

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
        #expect(got.teams == nil)
    }

    // askTeams decodes the teams of record apart from the people.
    @Test func askTeams() throws {
        let got: AskAnswer = try Core.decode(fixture("ask_teams"))
        #expect(got.people?.map(\.name) == ["Carla Counsel"])
        #expect(got.teams?.map(\.id) == ["codeowners:acme/legal-squad"])
        #expect(got.teams?.first?.name == "@acme/legal-squad")
        #expect(got.teams?.first?.team == nil)
        #expect(got.teams?.first?.strength == 1)
        #expect(got.teams?.first?.reasons?.isEmpty == false)
    }

    // askDeparted decodes the deactivated people apart from the people, each marked, and an
    // active person carries no mark.
    @Test func askDeparted() throws {
        let got: AskAnswer = try Core.decode(fixture("ask_departed"))
        #expect(got.people?.map(\.name) == ["Nora Here"])
        #expect(got.people?.first?.isDeactivated == false)
        #expect(got.departed?.map(\.name) == ["Lena Gone"])
        #expect(got.departed?.first?.isDeactivated == true)
        #expect(got.departed?.first?.role.hasPrefix("Deactivated, ") == true)
        #expect(got.people?.first?.role.contains("Deactivated") == false)
        let plain: AskAnswer = try Core.decode(fixture("ask"))
        #expect(plain.departed == nil)
        #expect(plain.people?.contains { $0.isDeactivated } == false)
    }

    // personDeparted decodes the deactivated mark on a person and keeps their Slack link out of
    // the contact buttons, while an active person keeps theirs.
    @Test @MainActor func personDeparted() throws {
        let got: PersonDetail = try Core.decode(fixture("person_departed"))
        #expect(got.isDeactivated)
        #expect(got.role.hasPrefix("Deactivated, "))
        #expect(got.links?.contains { $0.hasPrefix("slack:") } == true)
        #expect(SearchModel.contactLinks(got.links ?? [], deactivated: got.isDeactivated).isEmpty)
        #expect(SearchModel.contactLinks(got.links ?? [], deactivated: false) == got.links)
        let plain: PersonDetail = try Core.decode(fixture("person"))
        #expect(!plain.isDeactivated)
    }

    // RoleCase is one role line built from a title, a team, and the deactivated mark.
    struct RoleCase: Sendable {
        // Title is the job title, nil when not known.
        let Title: String?
        // Team is the team, nil when not known.
        let Team: String?
        // Deactivated marks a person who can no longer be reached.
        let Deactivated: Bool
        // WantRole is the expected line.
        let WantRole: String
    }

    // roleLine leads with Deactivated so a narrow window never cuts the mark, and skips what
    // is missing or empty.
    @Test(arguments: [
        // Test 0: Active with title and team.
        RoleCase(Title: "Staff Engineer", Team: "Payments", Deactivated: false, WantRole: "Staff Engineer, Payments"),
        // Test 1: Deactivated leads.
        RoleCase(Title: "Senior Software Engineer", Team: "Data", Deactivated: true,
                 WantRole: "Deactivated, Senior Software Engineer, Data"),
        // Test 2: Deactivated with nothing else known.
        RoleCase(Title: nil, Team: "", Deactivated: true, WantRole: "Deactivated"),
        // Test 3: Empty title skipped.
        RoleCase(Title: "", Team: "Payments", Deactivated: false, WantRole: "Payments"),
        // Test 4: Nothing to show.
        RoleCase(Title: nil, Team: nil, Deactivated: false, WantRole: ""),
    ])
    func roleLine(test: RoleCase) {
        #expect(Role.line(title: test.Title, team: test.Team, deactivated: test.Deactivated) == test.WantRole)
    }

    // peopleDeparted decodes the mark on the People sheet and writes it into the role line.
    @Test func peopleDeparted() throws {
        let got: PeopleList = try Core.decode(fixture("people_departed"))
        let lena = try #require(got.people.first { $0.name == "Lena Gone" })
        let nora = try #require(got.people.first { $0.name == "Nora Here" })
        #expect(lena.isDeactivated && lena.role.hasPrefix("Deactivated, "))
        #expect(!nora.isDeactivated && !nora.role.contains("Deactivated"))
    }

    // statusDeparted decodes the deactivated count, absent when nobody is.
    @Test func statusDeparted() throws {
        let got: IndexStatus = try Core.decode(fixture("status_departed"))
        #expect(got.people == 2)
        #expect(got.deactivated == 1)
        let plain: IndexStatus = try Core.decode(fixture("status"))
        #expect(plain.deactivated == nil)
    }

    // search decodes matches with their kind.
    @Test func search() throws {
        let got: SearchAnswer = try Core.decode(fixture("search"))
        #expect(got.results?.first?.id == "angela@example.com")
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
        #expect(got.person == "angela@example.com")
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
        #expect(git.found == Found(repos: 58, people: 1, channels: 0))
        let slack = try #require(got.scopes.last)
        #expect(slack.source == "slack")
        #expect(slack.ranAt == nil)
        #expect(slack.attemptedAt == nil)
        #expect(slack.error == nil)
    }

    // sample decodes the load result and a status that reads as sample data.
    @Test func sample() throws {
        let got: SampleResult = try Core.decode(fixture("import_sample"))
        #expect(got.people > 0)
        #expect(got.loadedAt.isEmpty == false)
        let status: IndexStatus = try Core.decode(fixture("status_sample"))
        #expect(status.sample)
        #expect(status.loadedAt?.isEmpty == false)
        #expect(status.scopes.count == 8)
        #expect(status.scopes.first?.scope == "sample")
        #expect(status.scopes.first?.ageSeconds == 180)
        let real: IndexStatus = try Core.decode(fixture("status"))
        #expect(!real.sample)
        #expect(real.loadedAt == nil)
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
        #expect(got.repos == 1)
        #expect(got.people == 2)
        #expect(got.found == Found(repos: 1, people: 2, channels: 0))
        #expect(got.finishLine == "Git: read 1 repository and found 2 people.")
    }

    // finishLine says what the run found in the source rows' words, and without found says one or
    // many in plain words and names a source other than git.
    @Test func finishLine() {
        let one = IndexResult(source: "git", scope: "repo:/a", repos: 1, people: 1, log: "")
        #expect(one.finishLine == "Git: read 1 repository. The index holds 1 person.")
        let many = IndexResult(source: "git", scope: "repo:/src", repos: 58, people: 3, log: "")
        #expect(many.finishLine == "Git: read 58 repositories. The index holds 3 people.")
        let csv = IndexResult(source: "org-csv", scope: "file:/a.csv", repos: 0, people: 40, log: "")
        #expect(csv.finishLine == "Org chart CSV: indexed. The index holds 40 people.")
        let git = IndexResult(source: "git", scope: "repo:/src", repos: 58, people: 6, log: "",
                              found: Found(repos: 58, people: 1, channels: 0))
        #expect(git.finishLine == "Git: read 58 repositories and found 1 person.")
        let slack = IndexResult(source: "slack", scope: "workspace:T1", repos: 0, people: 6, log: "",
                                found: Found(repos: 0, people: 5, channels: 1))
        #expect(slack.finishLine == "Slack: found 5 people in 1 channel.")
    }

    // refresh decodes each scope's outcome.
    @Test func refresh() throws {
        let got: RefreshResult = try Core.decode(fixture("refresh"))
        #expect(got.ok)
        #expect(got.results.first?.ranAt.isEmpty == false)
        #expect(got.results.first?.found == Found(repos: 1, people: 2, channels: 0))
        #expect(got.results.first?.line == "Git: read 1 repository again and found 2 people.")
    }

    // refreshLine says what each source found in plain words, or why it failed.
    @Test func refreshLine() {
        func scope(_ source: String, ok: Bool = true, error: String? = nil, found: Found?) -> RefreshScope {
            RefreshScope(source: source, scope: "s", ranAt: "", ok: ok, error: error, found: found)
        }
        #expect(scope("slack", found: Found(repos: 0, people: 5, channels: 1)).line
            == "Slack: refreshed and found 5 people in 1 channel.")
        #expect(scope("git", found: Found(repos: 58, people: 1, channels: 0)).line
            == "Git: read 58 repositories again and found 1 person.")
        #expect(scope("org-csv", found: Found(repos: 0, people: 0, channels: 0)).line
            == "Org chart CSV: refreshed and found no one.")
        #expect(scope("jira", found: nil).line == "Jira: refreshed.")
        #expect(scope("jira", ok: false, error: "bad token", found: nil).line == "Jira: bad token.")
    }

    // people decodes everyone the index holds with team names and links.
    @Test func people() throws {
        let got: PeopleList = try Core.decode(fixture("people"))
        #expect(got.people.map(\.name) == ["Angela Malone", "Bob Smith", "Carol Lee"])
        let angela = try #require(got.people.first)
        #expect(angela.id == "angela@example.com")
        #expect(angela.team == "Payments")
        #expect(angela.role == "Staff Engineer, Payments")
        #expect(angela.links.first?.hasPrefix("slack://user?") == true)
    }

    // peopleFilter keeps a match on name, title, or team in any case, and drops a miss.
    @Test func peopleFilter() {
        let ann = ListedPerson(id: "ann@x.com", name: "Ann Lee", title: "Staff Engineer", team: "Payments",
                               links: [])
        #expect(ann.matches("payments"))
        #expect(ann.matches("STAFF"))
        #expect(ann.matches("ann"))
        #expect(ann.matches(" "))
        #expect(!ann.matches("billing"))
        let bare = ListedPerson(id: "bo@x.com", name: "Bo", title: "", team: "", links: [])
        #expect(bare.role.isEmpty)
        #expect(!bare.matches("payments"))
    }

    // countList joins parts the way a sentence lists them.
    @Test func countList() {
        #expect(Count.list([]) == "")
        #expect(Count.list(["1 person"]) == "1 person")
        #expect(Count.list(["1 person", "1 channel"]) == "1 person and 1 channel")
        #expect(Count.list(["6 people", "2 teams", "1 channel"]) == "6 people, 2 teams, and 1 channel")
        #expect(Count.words(1, "person", "people") == "1 person")
        #expect(Count.words(0, "person", "people") == "0 people")
    }

    // wipe decodes the files removed.
    @Test func wipe() throws {
        let got: WipeResult = try Core.decode(fixture("wipe"))
        #expect(got.removed?.isEmpty == false)
    }
}
