package simorg

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/model"
)

// Spec describes an organization to synthesize. The zero value is a small but
// complete company; every field scales one dimension independently, so a test
// can grow the part it cares about without paying for the rest.
type Spec struct {
	// People is how many humans work here.
	People int
	// Channels is how many places they talk in.
	Channels int
	// Topics is how many subjects the company has. Each one gets an owner who
	// is the correct answer to a question about it.
	Topics int
	// ThreadsPerChannel is how many problems get worked through in each
	// channel. Each thread is a recall question with a known answer.
	ThreadsPerChannel int
	// ChatterPerChannel is how many loose off-topic messages surround them,
	// which is the noise ranking has to see past.
	ChatterPerChannel int
	// NoArchive builds an index without the archive: thread replies still fold into the
	// searchable body, but nothing is retained verbatim. It measures the recall
	// a first-time user gets, which the archived build does not.
	NoArchive bool
	// Seed fixes the randomness so a run is reproducible.
	Seed int64
	// GivenNames and FamilyNames override the built-in name pools when set, so a
	// caller such as the demo can cast the company from its own list. Empty uses
	// the defaults.
	GivenNames  []string
	FamilyNames []string
}

// withDefaults fills a spec out into a small company.
func (s Spec) withDefaults() Spec {
	if s.People <= 0 {
		s.People = 40
	}
	if s.Channels <= 0 {
		s.Channels = 8
	}
	if s.Topics <= 0 {
		s.Topics = 12
	}
	if s.ThreadsPerChannel <= 0 {
		s.ThreadsPerChannel = 6
	}
	if s.ChatterPerChannel <= 0 {
		s.ChatterPerChannel = 20
	}
	if s.Seed == 0 {
		s.Seed = 1
	}
	return s
}

// Kind distinguishes what a generated question is asking for.
type Kind int

const (
	// KindWhoKnows asks who to talk to about a subject. The answer is the
	// person who owns it.
	KindWhoKnows Kind = iota
	// KindRecall asks about a conversation someone took part in. The answer is
	// that conversation, and it is only ever asked of a participant.
	KindRecall
	// KindAnchored asks about a subject the way a person asks months later:
	// one remembered word, the rest described in their own words.
	KindAnchored
	// KindBlind asks with no vocabulary in common with the subject at all. It
	// is the case pure word matching cannot win, and the reason semantic mode
	// and an LLM exist.
	KindBlind
)

// Question is something the generated company has a known right answer to,
// because the generator planted the answer before asking.
type Question struct {
	// Kind is what the question asks for.
	Kind Kind
	// Text is the question as a person would type it.
	Text string
	// WantPerson is the person who should come back first.
	WantPerson model.ID
	// Asker is who is asking, for recall questions.
	Asker model.ID
	// WantEpisode is the conversation that should come back, for recall.
	WantEpisode string
	// WantChannel is the channel that should come back first when the
	// question is routed to a place rather than a person. It is set only when
	// a channel is named for this question's subject.
	WantChannel string
}

// Org is a synthesized company: fake APIs the real connectors can read, and
// the answers the company was built to have.
type Org struct {
	// Spec is what was asked for.
	Spec Spec
	// Slack serves the generated workspace in Slack's wire format.
	Slack *httptest.Server
	// CSV is the org chart, in the format the org-csv source reads.
	CSV string
	// Questions are the questions with known answers.
	Questions []Question
	// Messages counts the Slack messages generated, for reporting scale.
	Messages int
}

// Close shuts down the servers.
func (o *Org) Close() {
	if o.Slack != nil {
		o.Slack.Close()
	}
}

// subjects are the topics a synthesized company works on. Each becomes a
// vocabulary an owner is fluent in and everyone else is not.
var subjects = []struct {
	// Topic is the subject name, used in questions.
	Topic string
	// Words are the terms people use when they discuss it. Every list
	// contains the words of its own topic name and shares no word, nor any
	// word that stems to the same root, with another list. That disjointness
	// is what makes a single owner the provably correct answer: a miss is
	// whotoask failing to find them, never two people having a fair claim.
	Words []string
	// Anchored is how someone asks months later, remembering one word of the
	// subject and describing the rest in their own words. This is the normal
	// case, not the friendly one.
	Anchored string
	// Blind is how someone asks who remembers the problem but none of the
	// vocabulary. No word in it appears anywhere in any subject, which is the
	// hardest thing to ask a matcher that works on words.
	Blind string
}{
	{"billing retries", []string{"billing", "retries", "dunning", "invoice", "chargeback", "proration"},
		"billing keeps breaking for some customers", "money we failed to collect from a customer"},
	{"kafka lag", []string{"kafka", "lag", "partition", "rebalance", "offset", "broker"},
		"kafka is falling behind again", "the event stream keeps falling behind"},
	{"certificate renewal", []string{"certificate", "renewal", "certbot", "expiry", "wildcard", "acme"},
		"the certificate broke on staging", "https stopped working on staging"},
	{"sso login", []string{"sso", "login", "saml", "oidc", "mfa", "idp"},
		"people cannot get through login", "nobody can sign in this morning"},
	{"kubernetes deploys", []string{"kubernetes", "deploys", "kubectl", "manifest", "helm", "namespace"},
		"kubernetes rollouts keep stalling", "shipping containers to the cluster fails"},
	{"database migrations", []string{"database", "migrations", "schema", "postgres", "backfill", "vacuum"},
		"migrations are locking things up", "changing table structure without downtime"},
	{"search indexing", []string{"search", "indexing", "relevance", "shard", "analyzer", "synonym"},
		"search results come back wrong", "results are wrong when people look things up"},
	{"payment webhooks", []string{"payment", "webhooks", "signature", "idempotency", "stripe", "callback"},
		"webhooks arrive twice", "notices from the card processor arrive twice"},
	{"cdn caching", []string{"cdn", "caching", "purge", "edge", "ttl", "origin"},
		"caching serves the wrong thing", "stale assets served to visitors"},
	{"terraform state", []string{"terraform", "state", "drift", "workspace", "tfstate", "hcl"},
		"terraform is out of sync", "our infrastructure code file drifted apart"},
	{"oncall paging", []string{"oncall", "paging", "escalation", "pagerduty", "runbook", "severity"},
		"paging goes to the wrong person", "who gets woken up at night"},
	{"mobile releases", []string{"mobile", "releases", "testflight", "crashlytics", "appstore", "ipa"},
		"mobile builds keep failing", "shipping the phone app to users"},
	{"data warehouse", []string{"data", "warehouse", "snowflake", "dbt", "freshness", "mart"},
		"warehouse numbers look stale", "nightly reporting numbers look old"},
	{"feature flags", []string{"feature", "flags", "toggle", "cohort", "targeting", "variant"},
		"flags are not rolling out right", "turning things on for some users only"},
	{"api ratelimits", []string{"api", "ratelimits", "throttle", "quota", "burst", "backpressure"},
		"we keep hitting ratelimits", "too many requests are being rejected"},
	{"image uploads", []string{"image", "uploads", "thumbnail", "resize", "presigned", "exif"},
		"uploads come out broken", "pictures people attach are broken"},
	// Not everything a company knows is engineering, and the everyday questions
	// are the ones people actually ask first. A company that can only answer
	// about kafka is not a company. Vocabularies stay disjoint from each other
	// and from the team names for the same reason the rest do: so exactly one
	// person is the right answer.
	{"vacation", []string{"vacation", "pto", "holiday", "leave", "accrual", "unpaid"},
		"how vacation days work here", "taking a few weeks off in the summer"},
	{"health benefits", []string{"benefits", "health", "insurance", "dental", "enrollment", "premiums"},
		"the benefits enrollment window", "adding a newborn to the family plan"},
	{"payroll taxes", []string{"payroll", "taxes", "paycheck", "withholding", "stub", "w2"},
		"something looks wrong on my paycheck", "the money that arrives every two weeks"},
	{"expense reports", []string{"expenses", "reports", "reimbursement", "receipts", "mileage", "perdiem"},
		"getting expenses reimbursed", "paying myself back for a work trip"},
	{"laptop hardware", []string{"laptop", "hardware", "keyboard", "warranty", "loaner", "docking"},
		"my laptop needs replacing", "the machine on my desk died"},
	{"onboarding paperwork", []string{"onboarding", "paperwork", "orientation", "badge", "buddy", "checklist"},
		"what a new hire does on day one", "somebody starting next monday"},
	{"hiring interviews", []string{"hiring", "interviews", "candidates", "recruiter", "offers", "panel"},
		"running an interview loop", "picking between two people we want"},
	{"office facilities", []string{"office", "facilities", "parking", "desks", "kitchen", "visitors"},
		"booking a desk in the office", "where guests check in when they arrive"},
	{"contract review", []string{"contracts", "review", "nda", "clause", "redline", "indemnity"},
		"getting a contract looked over", "the signoff before we can share anything"},
}

// teams are the organizational units people are sorted into. None of them
// shares a word with any subject, on purpose. A team name is a strong explicit
// signal in ranking, far stronger than a passing mention, so a team called
// Search would make everyone on it a better answer for "search indexing" than
// the person who actually owns it. That is whotoask behaving correctly, and it
// would make a scored miss meaningless.
var teams = []string{
	"Ledger", "Insights", "Infrastructure", "Security",
	"Discovery", "Devices", "Growth", "Developer Experience",
	// The rest of a company. Without these the org chart is all engineers and
	// nobody can answer where the vacation policy lives.
	"People", "Finance", "Workplace", "Talent", "Legal",
}

// givenNames and familyNames build people, including non-ASCII names so
// folding and identity joins are exercised at scale rather than in one case.
var (
	givenNames = []string{
		"Angela", "Bob", "Carol", "Dan", "Eve", "Frank", "Grace", "Heidi",
		"Ivan", "Judy", "Kevin", "Linda", "Mallory", "Niaj", "Olivia", "Peggy",
		"José", "Zoë", "Renée", "Søren", "Ana", "Chen", "Priya", "Omar",
	}
	familyNames = []string{
		"Malone", "Smith", "Lee", "Park", "Ng", "Ito", "Kim", "Novak",
		"Garcia", "Okafor", "Müller", "Dubois", "Rossi", "Silva", "Haddad", "Nair",
	}
)

// showGivenNames and showFamilyNames cast the demo company from characters across
// The Office, Full House, Fresh Prince of Bel-Air, Silicon Valley, Breaking Bad,
// Better Call Saul, and Boardwalk Empire. The generator pairs a given name with a
// family name independently, so most people come out a recognizable-but-original
// mashup (Walter Halpert, Kim Tanner, Nacho Bachman) rather than a verbatim character.
var (
	showGivenNames = []string{
		"Michael", "Jim", "Pam", "Dwight", "Angela", "Kevin", "Oscar", "Stanley",
		"Holly", "Ryan", "Kelly", "Andy", "Phyllis", "Meredith", "Darryl", "Erin",
		"Danny", "Jesse", "Joey", "Rebecca", "Kimmy", "Stephanie", "Michelle", "Steve",
		"Will", "Carlton", "Philip", "Vivian", "Hilary", "Ashley", "Geoffrey",
		"Richard", "Erlich", "Dinesh", "Jared", "Gavin", "Monica", "Laurie", "Russ",
		"Walter", "Skyler", "Hank", "Marie", "Saul", "Gustavo", "Mike", "Hector",
		"Gale", "Jane", "Todd", "Jimmy", "Kim", "Chuck", "Howard", "Nacho", "Lalo",
		"Werner", "Nucky", "Margaret", "Chalky", "Gillian", "Owen", "Eli", "Meyer",
	}
	showFamilyNames = []string{
		"Scott", "Halpert", "Beesly", "Schrute", "Malone", "Martinez", "Hudson",
		"Kapoor", "Bernard", "Palmer", "Vance", "Bratton", "Flenderson", "Lewis",
		"Wallace", "Tanner", "Katsopolis", "Gladstone", "Gibbler", "Hale", "Smith",
		"Banks", "Butler", "Hendricks", "Bachman", "Gilfoyle", "Belson", "Bream",
		"Bighetti", "Hanneman", "Dunn", "White", "Pinkman", "Schrader", "Goodman",
		"Fring", "Ehrmantraut", "Salamanca", "Boetticher", "McGill", "Wexler",
		"Hamlin", "Varga", "Schweikart", "Thompson", "Darmody", "Schroeder", "Harrow",
		"Capone", "Rosetti", "Rothstein", "Luciano", "Lansky",
	}
)

// showCharacters are the people these two pools were taken apart from. The point
// of the pools is a recombination, so a generated name that lands back on one of
// the originals is a failure of the mixing rather than a bit of fun: the company
// is meant to be populated by Walter Scott and Michael White, never by Michael
// Scott. Any pairing listed here is skipped in favor of the next surname.
var showCharacters = map[string]bool{
	// The Office
	"Michael Scott": true, "Jim Halpert": true, "Pam Beesly": true,
	"Dwight Schrute": true, "Kevin Malone": true, "Oscar Martinez": true,
	"Stanley Hudson": true, "Kelly Kapoor": true, "Andy Bernard": true,
	"Meredith Palmer": true, "Phyllis Vance": true,
	// Full House
	"Danny Tanner": true, "Jesse Katsopolis": true, "Joey Gladstone": true,
	"Kimmy Gibbler": true, "Stephanie Tanner": true, "Michelle Tanner": true,
	"Steve Hale": true,
	// Fresh Prince of Bel-Air
	"Will Smith": true, "Carlton Banks": true, "Philip Banks": true,
	"Vivian Banks": true, "Hilary Banks": true, "Ashley Banks": true,
	"Geoffrey Butler": true,
	// Silicon Valley
	"Richard Hendricks": true, "Erlich Bachman": true, "Gavin Belson": true,
	"Laurie Bream": true, "Russ Hanneman": true, "Jared Dunn": true,
	// Breaking Bad
	"Walter White": true, "Skyler White": true, "Hank Schrader": true,
	"Marie Schrader": true, "Jesse Pinkman": true, "Saul Goodman": true,
	"Gustavo Fring": true, "Mike Ehrmantraut": true, "Hector Salamanca": true,
	"Gale Boetticher": true,
	// Better Call Saul
	"Jimmy McGill": true, "Kim Wexler": true, "Chuck McGill": true,
	"Howard Hamlin": true, "Nacho Varga": true, "Lalo Salamanca": true,
	// Boardwalk Empire
	"Nucky Thompson": true, "Margaret Schroeder": true, "Chalky White": true,
	"Gillian Darmody": true, "Jimmy Darmody": true, "Eli Thompson": true,
	"Meyer Lansky": true, "Richard Harrow": true,
}

// mixName pairs the given name at i with a surname, striding through the family
// pool rather than walking it in step so the two lists never line up, and
// skipping any pairing that would rebuild one of the originals.
func mixName(given, family []string, i int) (string, string) {
	g := given[i%len(given)]
	// A stride coprime with the pool length visits every surname before
	// repeating, which keeps the mix even instead of favoring a few pairs.
	start := (i*mixStride + i/len(given)) % len(family)
	for k := range family {
		f := family[(start+k)%len(family)]
		if !showCharacters[g+" "+f] {
			return g, f
		}
	}
	return g, family[start]
}

// mixStride steps through the surname pool. It is coprime with the pool size, so
// consecutive people do not draw neighboring surnames.
const mixStride = 17

// Generate synthesizes a company and the questions it answers. Nothing is
// random about the answers: an owner is made fluent in one subject and nobody
// else is, and every thread is planted with a person who resolves it, so a
// scorer can tell right from wrong without a human labeling anything.
func Generate(spec Spec) *Org {
	spec = spec.withDefaults()
	rng := rand.New(rand.NewSource(spec.Seed))

	people := generatePeople(spec, rng)
	owners := assignOwners(spec, people)
	channels := generateChannels(spec, owners)
	history, threads, count := generateHistory(spec, rng, people, owners, channels)

	org := &Org{
		Spec:     spec,
		CSV:      orgCSV(people),
		Messages: count,
		Slack:    slackServerFor(people, channels, history, threads),
	}
	org.Questions = append(questionsForOwners(owners), questionsForThreads(threads)...)
	return org
}

// person is one synthesized human.
type person struct {
	// id is the Slack user id.
	id string
	// name is the display name.
	name string
	// email is the work email, which is how sources join.
	email string
	// title is the job title.
	title string
	// team is the team name.
	team string
	// manager is the manager's email, empty at the top of the chain. Set only by
	// the demo company builder.
	manager string
	// github is the GitHub login, empty when the person has none and is joined by
	// alias instead. Set only by the demo company builder.
	github string
	// topics are the subject indices this person is an expert in. Set only by the
	// demo company builder.
	topics []int
	// deactivated marks a Slack account the workspace has deactivated. Set only
	// by the demo company builder.
	deactivated bool
}

// canonical returns the identifier the index keys this person by.
func (p person) canonical() model.ID { return model.ID(p.email) }

// generatePeople builds the workforce.
func generatePeople(spec Spec, rng *rand.Rand) []person {
	given := spec.GivenNames
	if len(given) == 0 {
		given = givenNames
	}
	family := spec.FamilyNames
	if len(family) == 0 {
		family = familyNames
	}
	out := make([]person, 0, spec.People)
	for i := range spec.People {
		g := given[i%len(given)]
		f := family[(i/len(given)+i)%len(family)]
		name := g + " " + f
		email := fmt.Sprintf("%s.%s%d@example.com",
			strings.ToLower(asciiFold(g)), strings.ToLower(asciiFold(f)), i)
		out = append(out, person{
			id:    fmt.Sprintf("U%04d", i),
			name:  name,
			email: email,
			title: []string{"Engineer", "Senior Engineer", "Staff Engineer", "Manager"}[rng.Intn(4)],
			team:  teams[i%len(teams)],
		})
	}
	return out
}

// asciiFold strips the accents the generator plants in names, so a generated
// email stays a plausible address.
func asciiFold(s string) string {
	repl := strings.NewReplacer(
		"é", "e", "ë", "e", "è", "e", "ö", "o", "ø", "o", "ü", "u", "í", "i", "á", "a")
	return repl.Replace(s)
}

// owner ties a subject to the one person who owns it.
type owner struct {
	// subject indexes into subjects.
	subject int
	// who owns it.
	who person
	// channel is where they mostly discuss it.
	channel string
}

// assignOwners gives each subject exactly one owner, so every who-knows
// question has one correct answer.
func assignOwners(spec Spec, people []person) []owner {
	n := min(spec.Topics, min(len(subjects), len(people)))
	out := make([]owner, 0, n)
	for i := range n {
		out = append(out, owner{
			subject: i,
			who:     people[i],
			channel: fmt.Sprintf("C%03d", i%spec.Channels),
		})
	}
	return out
}

// generateChannels builds the places people talk, named after the subject that
// dominates each one.
func generateChannels(spec Spec, owners []owner) []map[string]any {
	out := make([]map[string]any, 0, spec.Channels)
	for i := range spec.Channels {
		name, topic := fmt.Sprintf("team-%d", i), "general discussion"
		for _, o := range owners {
			if o.channel == fmt.Sprintf("C%03d", i) {
				name = strings.ReplaceAll(subjects[o.subject].Topic, " ", "-")
				topic = subjects[o.subject].Topic
				break
			}
		}
		out = append(out, slackChannel(fmt.Sprintf("C%03d", i), name, topic))
	}
	return out
}

// thread is a planted conversation with a known participant set.
type thread struct {
	// id is the episode id the pipeline will produce for it.
	id string
	// ts is the parent timestamp.
	ts string
	// problem is the question the thread opens with.
	problem string
	// asker is the person who raised it.
	asker person
	// helper is the person who resolved it.
	helper person
	// subject indexes into subjects.
	subject int
}

// generateHistory fills every channel with threads that resolve and chatter
// that does not, and returns the planted threads plus the message count.
func generateHistory(
	spec Spec, rng *rand.Rand, people []person, owners []owner, channels []map[string]any,
) (map[string][]map[string]any, []thread, int) {
	history := make(map[string][]map[string]any, len(channels))
	var threads []thread
	count := 0
	// History runs up to the present. An index decays what it reads by age, so
	// a corpus stamped years in the past tests how whotoask treats abandoned
	// archives rather than a living company: fluency written in 2020 loses to
	// a stray mention written a year later on decay alone.
	var ts int
	span := len(channels)*(spec.ChatterPerChannel+spec.ThreadsPerChannel*6+spec.Topics) + 64
	ts = int(time.Now().Unix()) - span*600 - 3600

	for i := range channels {
		channelID := fmt.Sprintf("C%03d", i)
		// A channel takes its character from the first owner who works there.
		// More subjects than channels means several owners share one, which is
		// what a real company looks like.
		subject := i % len(subjects)
		for _, o := range owners {
			if o.channel == channelID {
				subject = o.subject
				break
			}
		}
		words := subjects[subject].Words
		var msgs []map[string]any

		// Each owner is fluent in their own subject and says its words often,
		// which is the signal ranking is supposed to find. They speak their
		// own vocabulary, not the channel's, and they cover the whole of it:
		// random sampling can leave an owner never saying one of their own
		// words, at which point any chatterer who used it twice outranks them
		// on that word, which is not what fluency looks like anywhere real.
		for _, o := range owners {
			if o.channel != channelID {
				continue
			}
			ownWords := subjects[o.subject].Words
			for k := range ownerFluency {
				ts += 600
				msgs = append(msgs, slackMessageAt(o.who.id,
					sentenceCovering(rng, fillers.Owner, ownWords, k), ts))
				count++
			}
		}

		// Everyone else talks, shallowly. Half of it borrows some subject's
		// words, which is the chatterbox noise an owner has to beat; the rest
		// is the small talk of any workplace, which keeps the noise from
		// containing more domain vocabulary than the experts produce.
		for k := range spec.ChatterPerChannel {
			ts += 600
			p := people[rng.Intn(len(people))]
			other := smallTalk
			if k%2 == 0 {
				other = subjects[rng.Intn(len(subjects))].Words
			}
			msgs = append(msgs, slackMessageAt(p.id, sentence(rng, fillers.Chatter, other), ts))
			count++
		}

		// Threads: someone hits a problem and someone else resolves it. The
		// resolver is deliberately not the channel's owner, so recall cannot
		// be answered by guessing the owner.
		for t := range spec.ThreadsPerChannel {
			ts += 3600
			asker := people[rng.Intn(len(people))]
			helper := people[rng.Intn(len(people))]
			for helper.id == asker.id {
				helper = people[rng.Intn(len(people))]
			}
			marker := fmt.Sprintf("%s-%03d-%02d", words[0], i, t)
			problem := fmt.Sprintf("%s is acting up again, %s",
				marker, sentence(rng, fillers.Owner, words))
			parentTS := fmt.Sprintf("%d.000100", ts)
			msgs = append(msgs, slackThreadAt(asker.id, problem, ts, 2,
				[]string{helper.id}, ts+900))
			count += 3
			threads = append(threads, thread{
				id:      "slack:" + channelID + ":" + parentTS,
				ts:      parentTS,
				problem: marker,
				asker:   asker,
				helper:  helper,
				subject: subject,
			})
		}
		history[channelID] = msgs
	}
	return history, threads, count
}

// smallTalk is what chatter says when it is not borrowing a subject's words:
// the office noise of any company. None of these words shares a stem with any
// subject vocabulary, team, or title, for the same reason the fillers do not.
var smallTalk = []string{
	"coffee", "lunch", "weekend", "snacks", "elevator",
	"playlist", "weather", "birthday", "puzzle", "stairs",
}

// fillers are the connective words generated messages are phrased with. None
// of them shares a stem with any subject vocabulary, any team name, or any
// title, so they add sentence shape without adding signal: they are uniform
// noise to the keyword matcher and natural language to an embedding model.
var fillers = struct {
	// Owner phrases how someone fluent in a subject talks about it.
	Owner []string
	// Chatter phrases how a passerby mentions something once.
	Chatter []string
}{
	Owner: []string{
		"spent the morning on the %s %s again, sorted it by adjusting the %s",
		"heads up, the %s %s needs the %s bumped after yesterday",
		"wrote up how the %s ties into the %s and the %s",
		"if the %s acts odd, look at the %s before touching the %s",
	},
	Chatter: []string{
		"anyone seen the %s %s acting odd since this morning",
		"no idea about the %s, maybe ask whoever owns the %s",
		"saw something odd with the %s %s, ignoring it now",
	},
}

// ownerFluency is how many messages an owner writes about their own subject.
const ownerFluency = 12

// sentenceCovering renders a filler template like sentence, but fills the
// slots by rotating through the vocabulary from position k instead of sampling
// it, so across an owner's messages every word of their subject is guaranteed
// to be said several times.
func sentenceCovering(rng *rand.Rand, templates []string, words []string, k int) string {
	tpl := templates[rng.Intn(len(templates))]
	n := strings.Count(tpl, "%s")
	args := make([]any, n)
	for j := range n {
		args[j] = words[(k*2+j)%len(words)]
	}
	return fmt.Sprintf(tpl, args...)
}

// sentence renders a filler template with words from a vocabulary, so a
// generated message reads like something a person typed rather than a list of
// terms.
func sentence(rng *rand.Rand, templates []string, words []string) string {
	tpl := templates[rng.Intn(len(templates))]
	n := strings.Count(tpl, "%s")
	args := make([]any, n)
	for i := range n {
		args[i] = words[rng.Intn(len(words))]
	}
	return fmt.Sprintf(tpl, args...)
}

// questionsForOwners asks who knows each subject, where the owner is the
// answer by construction. A who-knows question also has a right place to ask:
// the channel named for the subject, when this subject is the one its channel
// was named for.
func questionsForOwners(owners []owner) []Question {
	namedFor := make(map[string]int)
	for _, o := range owners {
		if _, ok := namedFor[o.channel]; !ok {
			namedFor[o.channel] = o.subject
		}
	}
	out := make([]Question, 0, len(owners)*3)
	for _, o := range owners {
		subject := subjects[o.subject]
		wantChannel := ""
		if namedFor[o.channel] == o.subject {
			wantChannel = strings.ReplaceAll(subject.Topic, " ", "-")
		}
		out = append(out,
			Question{
				Kind:        KindWhoKnows,
				Text:        "who knows about " + subject.Topic,
				WantPerson:  o.who.canonical(),
				WantChannel: wantChannel,
			},
			Question{
				Kind:       KindAnchored,
				Text:       subject.Anchored,
				WantPerson: o.who.canonical(),
			},
			Question{
				Kind:       KindBlind,
				Text:       subject.Blind,
				WantPerson: o.who.canonical(),
			})
	}
	return out
}

// questionsForThreads asks each thread's asker about the problem they raised,
// where the answer is that conversation and the person who helped.
func questionsForThreads(threads []thread) []Question {
	out := make([]Question, 0, len(threads))
	for _, t := range threads {
		out = append(out, Question{
			Kind:        KindRecall,
			Text:        t.problem,
			Asker:       t.asker.canonical(),
			WantPerson:  t.helper.canonical(),
			WantEpisode: t.id,
		})
	}
	return out
}

// orgCSV renders the workforce as the org chart source reads it.
func orgCSV(people []person) string {
	var b strings.Builder
	b.WriteString("name,email,title,team,org,manager,topics\n")
	for _, p := range people {
		fmt.Fprintf(&b, "%s,%s,%s,%s,Engineering,,\n", p.name, p.email, p.title, p.team)
	}
	return b.String()
}

// slackServerFor serves a generated workspace in Slack's wire format, so the
// real connector ingests it without knowing it is synthetic.
func slackServerFor(
	people []person, channels []map[string]any,
	history map[string][]map[string]any, threads []thread,
) *httptest.Server {
	users := make([]map[string]any, 0, len(people))
	for _, p := range people {
		u := slackUser(p.id, p.name, p.email, p.title)
		if p.deactivated {
			u["deleted"] = true
		}
		users = append(users, u)
	}
	replies := make(map[string][]map[string]any, len(threads))
	for _, t := range threads {
		replies[t.ts] = []map[string]any{
			slackMessage(t.asker.id, t.problem+" is failing again", time.Now()),
			slackMessage(t.helper.id, "try clearing the "+subjects[t.subject].Words[1]+
				" and rerunning, that fixed it last time", time.Now()),
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "members": users})
	})
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "channels": channels})
	})
	mux.HandleFunc("/auth.test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"ok": true, "user_id": "U9999", "url": "https://generated.slack.com/", "team": "Generated",
		})
	})
	mux.HandleFunc("/conversations.replies", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		writeJSON(w, map[string]any{
			"ok": true, "has_more": false, "messages": replies[r.Form.Get("ts")],
		})
	})
	mux.HandleFunc("/conversations.history", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		writeJSON(w, map[string]any{
			"ok": true, "has_more": false, "messages": history[r.Form.Get("channel")],
		})
	})
	return httptest.NewServer(mux)
}

// slackMessageAt builds a message at an explicit epoch second, so generated
// history has a controlled shape in time.
func slackMessageAt(user, text string, sec int) map[string]any {
	return map[string]any{
		"type": "message", "user": user, "text": text,
		"ts": fmt.Sprintf("%d.000100", sec),
	}
}

// slackThreadAt builds a thread parent at an explicit epoch second.
func slackThreadAt(
	user, text string, sec, replyCount int, replyUsers []string, latest int,
) map[string]any {
	m := slackMessageAt(user, text, sec)
	m["thread_ts"] = m["ts"]
	m["reply_count"] = replyCount
	m["reply_users"] = replyUsers
	m["latest_reply"] = fmt.Sprintf("%d.000100", latest)
	return m
}
