package simorg

import (
	"context"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/confluence"
	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/github"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/jira"
	"github.com/dcadolph/whotoask/internal/pagerduty"
	"github.com/dcadolph/whotoask/internal/slack"
)

// BigSpec is the demo company's default shape: a few hundred people across many
// teams, cast from the show name pools, with enough history that recall, risk,
// and ownership all have something to find. It is the corpus `whotoask demo` serves.
func BigSpec() Spec {
	return Spec{
		People:            220,
		Channels:          len(subjects),
		Topics:            len(subjects),
		ThreadsPerChannel: 8,
		ChatterPerChannel: 30,
		Seed:              7,
		GivenNames:        showGivenNames,
		FamilyNames:       showFamilyNames,
	}
}

// icTitles are the individual-contributor titles the demo spreads across the
// workforce, so the org chart reads like a real engineering org rather than one
// role repeated.
var icTitles = []string{
	"Software Engineer", "Senior Software Engineer", "Staff Engineer",
	"Site Reliability Engineer", "Security Engineer", "Platform Engineer",
	"Analytics Engineer", "Principal Engineer",
}

// titlesByTeam gives the parts of the company that are not engineering their own
// roles. A People team staffed by site reliability engineers is a giveaway that
// the org chart was generated rather than observed.
// A title outranks a passing mention by a wide margin, so none of these may
// share a word with a subject: a "Payroll Specialist" would beat the person the
// company actually routes payroll questions to.
var titlesByTeam = map[string][]string{
	"People":    {"People Partner", "People Operations Specialist", "Employee Relations Lead"},
	"Finance":   {"Financial Analyst", "Accounting Specialist", "Procurement Lead"},
	"Workplace": {"Workplace Coordinator", "IT Support Specialist", "Workplace Manager"},
	"Talent":    {"Talent Partner", "Sourcer", "Talent Manager"},
	"Legal":     {"Attorney", "Compliance Analyst", "Privacy Lead"},
}

// titlePool returns the roles a team hires for.
func titlePool(team string) []string {
	if pool, ok := titlesByTeam[team]; ok {
		return pool
	}
	return icTitles
}

// company is a large synthesized organization for the public demo: people with a
// management chain and topic expertise, plus the Slack history that makes each
// owner the answer to their subject.
type company struct {
	spec     Spec
	people   []person
	owners   []owner
	channels []map[string]any
	history  map[string][]map[string]any
	threads  []thread
	messages int
}

// buildCompany synthesizes the demo organization deterministically from spec.
func buildCompany(spec Spec) *company {
	spec = spec.withDefaults()
	rng := rand.New(rand.NewSource(spec.Seed))
	people := buildBigPeople(spec)
	owners := buildBigOwners(people)
	channels := generateChannels(spec, owners)
	// The channel name is the topic slug; align each channel's topic and purpose
	// text to it so a channel does not also register its spaced phrase as separate
	// single-word topics.
	for _, ch := range channels {
		name, _ := ch["name"].(string)
		if tp, ok := ch["topic"].(map[string]any); ok {
			tp["value"] = name
		}
		if pp, ok := ch["purpose"].(map[string]any); ok {
			pp["value"] = name
		}
	}
	history, threads, count := generateHistory(spec, rng, people, owners, channels)
	c := &company{
		spec: spec, people: people, owners: owners,
		channels: channels, history: history, threads: threads, messages: count,
	}
	c.addSecondaryExperts(rng)
	return c
}

// departedSubject is the subject, kafka lag, whose one backup expert has a
// deactivated Slack account in the sample company.
const departedSubject = 1

// addSecondaryExperts gives some subjects a second or third fluent voice, so the
// risk view shows a real spread of critical, elevated, and ok rather than every
// topic sitting at bus factor one.
func (c *company) addSecondaryExperts(rng *rand.Rand) {
	// Recent, like the rest of the history: see generateHistory on decay.
	ts := int(time.Now().Unix()) - 60*24*3600
	for s := range c.owners {
		extra := s % 3 // 0 critical, 1 elevated, 2 ok
		channelID := c.owners[s].channel
		words := subjects[s].Words
		// A backup expert comes from the owner's own team when it has one:
		// the second person who can answer a vacation question works in
		// People, not on a randomly drawn engineering team.
		var teammates []int
		for i := range c.people {
			if c.people[i].team == c.owners[s].who.team && c.people[i].email != c.owners[s].who.email {
				teammates = append(teammates, i)
			}
		}
		for k := 0; k < extra; k++ {
			idx := (s*17 + k*53 + 11) % len(c.people)
			if len(teammates) > 0 {
				idx = teammates[(s+k)%len(teammates)]
			}
			if c.people[idx].email == c.owners[s].who.email {
				idx = (idx + 1) % len(c.people)
			}
			c.people[idx].topics = append(c.people[idx].topics, s)
			// The kafka lag backup has left, so the sample shows a departed
			// person who still knows a subject, ranked below the owner.
			if s == departedSubject && k == 0 {
				c.people[idx].deactivated = true
			}
			for range 5 {
				ts += 600
				c.history[channelID] = append(c.history[channelID],
					slackMessageAt(c.people[idx].id, sentence(rng, fillers.Owner, words), ts))
			}
		}
	}
}

// buildBigPeople casts the workforce and lays a four-level management chain over
// it: a VP, a few directors, one manager per team, and everyone else reporting to
// their team's manager.
func buildBigPeople(spec Spec) []person {
	given, family := spec.GivenNames, spec.FamilyNames
	if len(given) == 0 {
		given = showGivenNames
	}
	if len(family) == 0 {
		family = showFamilyNames
	}
	n := spec.People
	people := make([]person, n)
	for i := range n {
		g, f := mixName(given, family, i)
		email := fmt.Sprintf("%s.%s%d@example.com",
			strings.ToLower(asciiFold(g)), strings.ToLower(asciiFold(f)), i)
		people[i] = person{
			id:    fmt.Sprintf("U%04d", i),
			name:  g + " " + f,
			email: email,
			team:  teams[i%len(teams)],
			title: "Software Engineer",
		}
		// Most people have a GitHub login; a few have none and join by alias.
		if i%29 != 4 {
			people[i].github = strings.ToLower(asciiFold(g[:1]) + asciiFold(f) + fmt.Sprint(i))
		}
	}

	if n == 0 {
		return people
	}
	people[0].title = "VP of Engineering"
	var dirs []int
	for i := 1; i <= 3 && i < n; i++ {
		people[i].title = "Director of Engineering"
		people[i].manager = people[0].email
		dirs = append(dirs, i)
	}
	teamMgr := map[string]string{}
	// isMgr records who runs a team by index. Recognizing them by title instead
	// would miss every team whose manager is not titled "Engineering Manager",
	// and those people would fall through and be made their own manager.
	isMgr := make(map[int]bool)
	di := 0
	for i := 4; i < n; i++ {
		if _, ok := teamMgr[people[i].team]; ok {
			continue
		}
		people[i].title = "Engineering Manager"
		if _, ok := titlesByTeam[people[i].team]; ok {
			people[i].title = people[i].team + " Manager"
		}
		if len(dirs) > 0 {
			people[i].manager = people[dirs[di%len(dirs)]].email
		} else {
			people[i].manager = people[0].email
		}
		teamMgr[people[i].team] = people[i].email
		isMgr[i] = true
		di++
	}
	for i := 4; i < n; i++ {
		if isMgr[i] {
			continue
		}
		pool := titlePool(people[i].team)
		people[i].title = pool[i%len(pool)]
		if mgr, ok := teamMgr[people[i].team]; ok {
			people[i].manager = mgr
		} else {
			people[i].manager = people[0].email
		}
	}
	return people
}

// subjectTeams routes a subject to the part of the company that would really
// own it. A vacation question answered by a principal engineer reads as wrong
// even when the lookup worked, because in a real company that knowledge lives
// with the People team. Subjects not listed stay with engineering.
var subjectTeams = map[string]string{
	"vacation":             "People",
	"health benefits":      "People",
	"payroll taxes":        "Finance",
	"expense reports":      "Finance",
	"laptop hardware":      "Workplace",
	"office facilities":    "Workplace",
	"onboarding paperwork": "Talent",
	"hiring interviews":    "Talent",
	"contract review":      "Legal",
}

// buildBigOwners makes one person the expert for each subject, spread across the
// company so ownership is not clustered on a single team, and placed on the team
// that would own the subject in a real company.
func buildBigOwners(people []person) []owner {
	owners := make([]owner, 0, len(subjects))
	if len(people) == 0 {
		return owners
	}
	// Owners come from the fitting team when the subject has one. Managers are
	// skipped so the expert is the person who does the work, not the person the
	// work reports to.
	pickFromTeam := func(team string, nth int) int {
		var members []int
		for i := range people {
			if people[i].team == team && !strings.Contains(people[i].title, "Manager") &&
				!strings.Contains(people[i].title, "Director") && !strings.Contains(people[i].title, "VP") {
				members = append(members, i)
			}
		}
		if len(members) == 0 {
			return -1
		}
		return members[nth%len(members)]
	}
	perTeam := make(map[string]int)
	step := max(1, len(people)/len(subjects))
	for s := range subjects {
		idx := (s*step + 7) % len(people)
		if team, ok := subjectTeams[subjects[s].Topic]; ok {
			if i := pickFromTeam(team, perTeam[team]); i >= 0 {
				idx = i
				perTeam[team]++
			}
		}
		// A few neighbouring subjects deliberately fall to one person, which is
		// how a real company looks: somebody ends up holding a whole corner of
		// it. Combined with the work that crosses between them, that corner is
		// a joined body of work resting on one person, which is the heaviest
		// finding whotoask makes and had nothing to find in the sample company.
		if held, ok := heldTogether[s]; ok && len(owners) > held {
			idx = indexOf(people, owners[held].who)
		}
		people[idx].topics = append(people[idx].topics, s)
		owners = append(owners, owner{
			subject: s, who: people[idx], channel: fmt.Sprintf("C%03d", s),
		})
	}
	return owners
}

// heldTogether maps a subject to the earlier subject whose owner also takes it,
// so those subjects share one person. The keys must be consecutive with their
// value, since only neighbouring subjects have work crossing between them.
var heldTogether = map[int]int{1: 0, 2: 0, 3: 0}

// indexOf finds a person in the roster by email, or 0 if the roster is empty.
func indexOf(people []person, who person) int {
	for i := range people {
		if people[i].email == who.email {
			return i
		}
	}
	return 0
}

// orgCSV renders the company as the org-csv source reads it, with the management
// chain and topic expertise the small fixture leaves blank.
func (c *company) orgCSV() string {
	var b strings.Builder
	b.WriteString("name,email,title,team,org,manager,topics\n")
	for _, p := range c.people {
		names := make([]string, 0, len(p.topics))
		for _, t := range p.topics {
			names = append(names, topicSlug(t))
		}
		fmt.Fprintf(&b, "%s,%s,%s,%s,Engineering,%s,%s\n",
			p.name, p.email, p.title, p.team, p.manager, strings.Join(names, ";"))
	}
	return b.String()
}

// codeOwners maps a path per subject to a declared owner. Most declare the real
// expert; a planted fraction declare someone who no longer does the work, which
// is the ownership drift `whotoask ownership` surfaces.
func (c *company) codeOwners() string {
	var b strings.Builder
	for s := range subjects {
		declared := c.owners[s].who.email
		if s%5 == 0 {
			declared = c.people[(s*13+3)%len(c.people)].email
		} else if s%7 == 3 {
			// A slice of areas is declared by squad handle, the way a
			// monorepo's CODEOWNERS reads. The org chart names the same team,
			// so ownership judges these areas through the team's members
			// rather than setting them aside.
			declared = "@simcorp/" + teamSlug(c.owners[s].who.team) + "-squad"
		}
		path := strings.ReplaceAll(subjects[s].Topic, " ", "-") + "/"
		fmt.Fprintf(&b, "%s %s\n", path, declared)
	}
	return b.String()
}

// teamSlug renders a team name the way a CODEOWNERS handle spells it.
func teamSlug(team string) string {
	return strings.ReplaceAll(strings.ToLower(team), " ", "-")
}

// BuildBigIndex assembles the large demo company into a merged, canonicalized
// index under dir. It drives the org chart, expertise, and recall from one
// generated organization at scale.
func BuildBigIndex(dir string) (*index.Index, error) {
	return buildIndexFor(dir, BigSpec())
}

// buildIndexFor indexes a generated company of any size through the same eight
// connectors the demo uses, so the sources a scale run exercises are the ones
// the product actually reads.
func buildIndexFor(dir string, spec Spec) (*index.Index, error) {
	return buildIndexFrom(dir, spec, nil)
}

// buildIndexFrom indexes a generated company from a chosen subset of its
// sources. A nil set means all of them, which is every caller but the ablation
// that measures what each source is worth.
func buildIndexFrom(dir string, spec Spec, only map[string]bool) (*index.Index, error) {
	ctx := context.Background()
	c := buildCompany(spec)

	write := func(name, content string) (string, error) {
		p := filepath.Join(dir, name)
		return p, os.WriteFile(p, []byte(content), 0o600)
	}
	csvPath, err := write("org.csv", c.orgCSV())
	if err != nil {
		return nil, fmt.Errorf("simorg: %w", err)
	}
	ownersPath, err := write("CODEOWNERS", c.codeOwners())
	if err != nil {
		return nil, fmt.Errorf("simorg: %w", err)
	}

	repoDir := filepath.Join(dir, "repo")
	if err := c.buildGitRepo(repoDir); err != nil {
		return nil, err
	}

	slackSrv := c.slackServer()
	defer slackSrv.Close()
	githubSrv, repos := c.githubServer()
	defer githubSrv.Close()
	jiraSrv := c.jiraServer()
	defer jiraSrv.Close()
	confluenceSrv := c.confluenceServer()
	defer confluenceSrv.Close()
	pagerdutySrv := c.pagerdutyServer()
	defer pagerdutySrv.Close()

	sources := []struct {
		Name   string
		Source connector.Source
	}{
		{"org-csv", connector.NewOrgCSV(csvPath)},
		{"codeowners", connector.NewCodeOwners(ownersPath)},
		{"slack", connector.NewSlackWithClient(
			slack.New("xoxb-demo", slack.WithBaseURL(slackSrv.URL)), connector.SlackOptions{})},
		{"github", connector.NewGitHubWithClient(
			github.New("ghp-demo", github.WithBaseURL(githubSrv.URL)),
			connector.GitHubOptions{Repos: repos, ResolveEmails: true})},
		{"jira", connector.NewJiraWithClient(
			jira.New(jiraSrv.URL, "demo@example.com", "token"), connector.JiraOptions{})},
		{"confluence", connector.NewConfluenceWithClient(
			confluence.New(confluenceSrv.URL, "demo@example.com", "token"), connector.ConfluenceOptions{})},
		{"pagerduty", connector.NewPagerDutyWithClient(
			pagerduty.New("token", pagerduty.WithBaseURL(pagerdutySrv.URL)), connector.PagerDutyOptions{})},
		{"git", connector.NewGitHistory(connector.GitOptions{Paths: []string{repoDir}, SinceDays: 900})},
	}

	ix := index.New()
	for _, s := range sources {
		if only != nil && !only[s.Name] {
			continue
		}
		recs, err := s.Source.Fetch(ctx)
		if err != nil {
			return nil, fmt.Errorf("simorg: %s: %w", s.Name, err)
		}
		ix.Add(recs)
	}
	ix.AutoJoin()
	ix.Canonicalize()
	return ix, nil
}

// slackServer serves the company's generated workspace in Slack's wire format.
func (c *company) slackServer() *httptest.Server {
	return slackServerFor(c.people, c.channels, c.history, c.threads)
}

// BuildBigEpisodes collects the large demo company's recall material: the Slack
// conversations people worked through, resolved against ix so a person's work is
// findable. It mirrors BuildEpisodes for the big company.
func BuildBigEpisodes(ix *index.Index) (*episode.Store, error) {
	ctx := context.Background()
	c := buildCompany(BigSpec())
	slackSrv := c.slackServer()
	defer slackSrv.Close()
	githubSrv, repos := c.githubServer()
	defer githubSrv.Close()
	jiraSrv := c.jiraServer()
	defer jiraSrv.Close()
	pagerdutySrv := c.pagerdutyServer()
	defer pagerdutySrv.Close()

	sources := []struct {
		Name   string
		Source interface {
			connector.Source
			connector.EpisodeSource
		}
	}{
		{"slack", connector.NewSlackWithClient(
			slack.New("xoxb-demo", slack.WithBaseURL(slackSrv.URL)),
			connector.SlackOptions{Episodes: true, Archive: true})},
		{"github", connector.NewGitHubWithClient(
			github.New("ghp-demo", github.WithBaseURL(githubSrv.URL)),
			connector.GitHubOptions{Repos: repos, Episodes: true, ResolveEmails: true})},
		{"jira", connector.NewJiraWithClient(
			jira.New(jiraSrv.URL, "demo@example.com", "token"), connector.JiraOptions{Episodes: true})},
		{"pagerduty", connector.NewPagerDutyWithClient(
			pagerduty.New("token", pagerduty.WithBaseURL(pagerdutySrv.URL)),
			connector.PagerDutyOptions{Episodes: true})},
	}

	store := episode.New()
	for _, s := range sources {
		if _, err := s.Source.Fetch(ctx); err != nil {
			return nil, fmt.Errorf("simorg: %s episodes: %w", s.Name, err)
		}
		eps := s.Source.Episodes()
		if ix != nil {
			ix.CanonicalizeEpisodes(eps)
		}
		for _, ep := range eps {
			store.Add(ep)
		}
	}
	return store, nil
}

// BigDemoPerson is who the large demo opens as and the question it opens with: a
// person who raised a problem the company solved, so their recall view is not empty.
func BigDemoPerson() (email, query string) {
	c := buildCompany(BigSpec())
	query = "who knows about " + subjects[0].Topic
	switch {
	case len(c.threads) > 0:
		return c.threads[0].asker.email, query
	case len(c.people) > 0:
		return c.people[0].email, query
	default:
		return "", query
	}
}
