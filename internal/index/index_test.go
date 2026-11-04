package index

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dcadolph/peepwho/internal/connector"
)

// sampleRecords returns a small set of people for ranking tests.
func sampleRecords() []connector.Record {
	return []connector.Record{
		{Source: "t", Name: "Jane Roe", Email: "jane@x.com", Title: "Staff Engineer",
			Team: "Billing", Topics: []string{"retries", "idempotency"}},
		{Source: "t", Name: "Bob Lee", Email: "bob@x.com", Title: "SRE",
			Team: "Infra", Topics: []string{"kafka"}},
		{Source: "t", Name: "Carol Ng", Email: "carol@x.com", Title: "Engineer",
			Team: "Billing", Text: "owns the billing dashboards"},
		{Source: "t", Name: "Dana Fox", Email: "dana@x.com", Title: "Author of runbooks",
			Team: "Docs", Text: "auth service reviews"},
	}
}

// TestSearchRanking verifies the most relevant person ranks first with reasons.
func TestSearchRanking(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name       string
		Query      string
		WantTop    string
		WantReason string
	}{
		{Name: "topic", Query: "retries", WantTop: "jane@x.com", WantReason: "retries (topic)"},
		{Name: "team text", Query: "billing dashboards", WantTop: "carol@x.com",
			WantReason: "dashboards (mention)"},
		{Name: "title", Query: "sre", WantTop: "bob@x.com", WantReason: "sre (title)"},
		{Name: "inflected topic", Query: "retry", WantTop: "jane@x.com",
			WantReason: "retry (topic)"},
		{Name: "no substring inflation", Query: "auth", WantTop: "dana@x.com",
			WantReason: "auth (mention)"},
		{Name: "fuzzy topic typo", Query: "kafkaa", WantTop: "bob@x.com",
			WantReason: "kafka (topic, read for \"kafkaa\")"},
	}

	ix := New()
	ix.Build(sampleRecords())

	for testNum, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			got := ix.Search(test.Query, 5)
			if len(got) == 0 {
				t.Fatalf("test %d: no matches for %q", testNum, test.Query)
			}
			if string(got[0].Person.ID) != test.WantTop {
				t.Errorf("test %d: top = %q, want %q", testNum, got[0].Person.ID, test.WantTop)
			}
			if !slices.Contains(got[0].Reasons, test.WantReason) {
				t.Errorf("test %d: reasons %v missing %q", testNum, got[0].Reasons, test.WantReason)
			}
		})
	}
}

// TestFuzzyMatching verifies typo tolerance: a close typo resolves with a
// score penalty so exact stays ahead, short terms never fuzz, and nonsense
// still matches nothing.
func TestFuzzyMatching(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build(sampleRecords())

	exact := ix.Search("kafka", 5)
	typo := ix.Search("kafkaa", 5)
	if len(exact) == 0 || len(typo) == 0 {
		t.Fatalf("exact = %d results, typo = %d results, want both to match", len(exact), len(typo))
	}
	if typo[0].Person.ID != "bob@x.com" {
		t.Errorf("typo top = %s, want bob@x.com", typo[0].Person.ID)
	}
	if typo[0].Score >= exact[0].Score {
		t.Errorf("typo score %.3f not below exact %.3f", typo[0].Score, exact[0].Score)
	}
	if got := ix.Search("kfk", 5); len(got) != 0 {
		t.Errorf("short typo matched %d results, want none", len(got))
	}
	if got := ix.Search("zzzzzz", 5); len(got) != 0 {
		t.Errorf("nonsense matched %d results, want none", len(got))
	}
}

// TestStrengthIgnoresScaffold verifies conversational scaffolding such as
// "who knows" does not dilute strength: a dead-on topic owner scores full
// strength for the tagline phrasing.
func TestStrengthIgnoresScaffold(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build(sampleRecords())
	got := ix.Search("who knows retries", 5)
	if len(got) == 0 {
		t.Fatal("no matches")
	}
	if got[0].Person.ID != "jane@x.com" || got[0].Strength != 1.0 {
		t.Errorf("top = %s strength = %v, want jane@x.com at 1.0", got[0].Person.ID, got[0].Strength)
	}
}

// TestDuplicateTermsKeepStrength verifies a repeated query token neither
// deflates coverage nor double-counts, so it ranks identically to one token.
func TestDuplicateTermsKeepStrength(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build(sampleRecords())
	once := ix.Search("retries", 5)
	twice := ix.Search("retries retries", 5)
	if len(once) == 0 || len(twice) == 0 {
		t.Fatal("no matches")
	}
	if once[0].Person.ID != twice[0].Person.ID {
		t.Fatalf("top differs: %s vs %s", once[0].Person.ID, twice[0].Person.ID)
	}
	if once[0].Strength != twice[0].Strength || once[0].Score != twice[0].Score {
		t.Errorf("duplicate token changed ranking: strength %v vs %v, score %v vs %v",
			once[0].Strength, twice[0].Strength, once[0].Score, twice[0].Score)
	}
}

// TestLoadPartialSnapshotMerges verifies a current-schema snapshot whose graph
// omits sub-maps, the shape an empty index leaves behind omitempty, does not
// panic when records are merged onto it.
func TestLoadPartialSnapshotMerges(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.json")
	partial := fmt.Sprintf(`{"schema":%d,"graph":{}}`, snapshotSchema)
	if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	ix, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ix.Add(sampleRecords())
	if len(ix.Search("retries", 5)) == 0 {
		t.Error("want matches after merging onto a partial snapshot")
	}
}

// TestSearchEmpty verifies a stopword-only query yields nothing.
func TestSearchEmpty(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build(sampleRecords())
	if got := ix.Search("who do I talk to about", 5); len(got) != 0 {
		t.Errorf("got %d matches, want 0", len(got))
	}
}

// TestSaveLoad verifies an index survives a round trip to disk.
func TestSaveLoad(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build(sampleRecords())

	path := filepath.Join(t.TempDir(), "index.json")
	if err := ix.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	got := loaded.Search("retries", 1)
	if len(got) != 1 || string(got[0].Person.ID) != "jane@x.com" {
		t.Fatalf("after load, top match = %v, want jane@x.com", got)
	}
	if got[0].Team == nil || got[0].Team.Name != "Billing" {
		t.Errorf("team not restored: %+v", got[0].Team)
	}
}

// TestTokenize covers lowercasing, splitting, and stopword removal.
func TestTokenize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want []string
	}{
		{In: "Who do I talk to about Billing-Retries?", Want: []string{"billing", "retries"}},
		{In: "Kafka, Kubernetes & SRE", Want: []string{"kafka", "kubernetes", "sre"}},
		{In: "a I to the", Want: nil},
	}
	for testNum, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(test.Want, tokenize(test.In), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("test %d: mismatch (-want +got):\n%s", testNum, diff)
			}
		})
	}
}

// TestSlug covers identifier normalization.
func TestSlug(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want string
	}{
		{In: "Site Reliability Eng!", Want: "site-reliability-eng"},
		{In: "  Billing / Payments  ", Want: "billing-payments"},
		{In: "A_B-C", Want: "a-b-c"},
	}
	for testNum, test := range tests {
		t.Run(test.In, func(t *testing.T) {
			t.Parallel()
			if got := slug(test.In); got != test.Want {
				t.Errorf("test %d: slug(%q) = %q, want %q", testNum, test.In, got, test.Want)
			}
		})
	}
}

// TestSearchChannels verifies channel ranking, reasons, and member ordering.
func TestSearchChannels(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Title: "Engineer", Topics: []string{"retries"}},
		{Name: "Bob Lee", Email: "bob@x.com", Title: "SRE", Topics: []string{"kafka"}},
		{Kind: connector.KindChannel, Name: "billing", Title: "retries and dunning",
			Members: []string{"jane@x.com", "bob@x.com"}, Text: "billing platform"},
	})

	got := ix.SearchChannels("retries", 5)
	if len(got) == 0 {
		t.Fatal("no channel matches for retries")
	}
	if got[0].Channel.Name != "billing" {
		t.Errorf("top channel = %q, want billing", got[0].Channel.Name)
	}
	if !slices.Contains(got[0].Reasons, "retries (topic)") {
		t.Errorf("reasons %v missing retries (topic)", got[0].Reasons)
	}
	if len(got[0].TopMembers) == 0 || got[0].TopMembers[0].Email != "jane@x.com" {
		t.Errorf("top member = %v, want jane@x.com first", got[0].TopMembers)
	}
}

// TestStemMatching verifies a query matches indexed terms across word forms.
func TestStemMatching(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Topics: []string{"scanning"}},
	})
	for _, q := range []string{"scans", "scan", "scanning"} {
		got := ix.Search(q, 1)
		if len(got) != 1 || got[0].Person.Email != "jane@x.com" {
			t.Errorf("query %q: got %v, want jane@x.com", q, got)
		}
	}
}

// TestAddMerges verifies Add accumulates onto an existing index: it keeps old
// signal, extends a person shared by email, and adds new people.
func TestAddMerges(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Name: "Jane", Email: "jane@x.com", Topics: []string{"billing"}},
	})
	ix.Add([]connector.Record{
		{Name: "Jane Roe", Email: "jane@x.com", Topics: []string{"retries"}},
		{Name: "Bob", Email: "bob@x.com", Topics: []string{"kafka"}},
	})

	if got := ix.Search("billing", 1); len(got) != 1 || got[0].Person.Email != "jane@x.com" {
		t.Errorf("billing kept after merge: %v", got)
	}
	if got := ix.Search("retries", 1); len(got) != 1 || got[0].Person.Email != "jane@x.com" {
		t.Errorf("jane gained retries from merge: %v", got)
	}
	if got := ix.Search("kafka", 1); len(got) != 1 || got[0].Person.Email != "bob@x.com" {
		t.Errorf("bob added by merge: %v", got)
	}
}

// TestProfile verifies the full-person lookup resolves aliases and gathers
// org placement and channel membership.
func TestProfile(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Alias("alice@corp.com", "github:alice")
	ix.Build([]connector.Record{{
		Kind: connector.KindPerson, Email: "alice@corp.com", Name: "Alice Smith",
		Title: "Engineer", Team: "Payments", Org: "Corp", Manager: "boss@corp.com",
		Topics: []string{"billing"}, Source: "org-csv",
	}, {
		Kind: connector.KindPerson, Email: "boss@corp.com", Name: "Boss", Source: "org-csv",
	}, {
		Kind: connector.KindChannel, Name: "payments", Title: "billing questions",
		Members: []string{"alice@corp.com"}, Source: "slack",
	}})

	got, ok := ix.Profile("github:alice")
	if !ok {
		t.Fatal("Profile(github:alice) not found; alias should resolve")
	}
	if got.Person.Name != "Alice Smith" || got.Team == nil || got.Team.Name != "Payments" {
		t.Errorf("profile person/team = %+v / %+v", got.Person, got.Team)
	}
	if got.Org == nil || got.Org.Name != "Corp" {
		t.Errorf("profile org = %+v", got.Org)
	}
	if got.Manager == nil || got.Manager.Name != "Boss" {
		t.Errorf("profile manager = %+v", got.Manager)
	}
	if len(got.Channels) != 1 || got.Channels[0].Name != "payments" {
		t.Errorf("profile channels = %+v", got.Channels)
	}
	if _, ok := ix.Profile("nobody@corp.com"); ok {
		t.Error("Profile(nobody) = ok, want false")
	}
}

// TestMergeIsIdempotent verifies re-reading a source replaces its earlier
// contribution instead of stacking a second copy on top. The command peepwho's
// own help recommends for a schedule is `index --source X --merge`, so a run
// that inflated its own weights would drift the ranking a little further every
// night until an explicit topic owner lost to a passing mention.
func TestMergeIsIdempotent(t *testing.T) {
	t.Parallel()
	base := []connector.Record{
		{Source: "org-csv", Name: "Owner", Email: "owner@x.com", Topics: []string{"billing"}},
		{Source: "org-csv", Name: "Bystander", Email: "bystander@x.com"},
	}
	slack := []connector.Record{
		{Source: "slack", Email: "bystander@x.com", Text: "billing"},
		{Source: "slack", Email: "owner@x.com", Text: "deploy pipeline dashboard"},
	}

	once := New()
	once.Build(base)
	once.Add(slack)
	want := once.Search("billing", 5)

	many := New()
	many.Build(base)
	for range 20 {
		many.Add(slack)
	}
	got := many.Search("billing", 5)

	if len(got) != len(want) {
		t.Fatalf("after 20 merges: %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Person.ID != want[i].Person.ID {
			t.Errorf("rank %d after 20 merges = %s, want %s",
				i, got[i].Person.ID, want[i].Person.ID)
		}
		if math.Abs(got[i].Score-want[i].Score) > 1e-9 {
			t.Errorf("rank %d score after 20 merges = %.6f, want %.6f (weights inflated)",
				i, got[i].Score, want[i].Score)
		}
	}
	if len(got) > 0 && got[0].Person.ID != "owner@x.com" {
		t.Errorf("top result after 20 merges = %s, want the topic owner", got[0].Person.ID)
	}
}

// TestMergeReplacesOnlyItsOwnSource verifies merging one source leaves the
// others exactly as they were, so re-reading Slack does not disturb the org
// chart or the tickets already indexed.
func TestMergeReplacesOnlyItsOwnSource(t *testing.T) {
	t.Parallel()
	ix := New()
	ix.Build([]connector.Record{
		{Source: "org-csv", Name: "Jane", Email: "jane@x.com", Topics: []string{"billing"}},
	})
	ix.Add([]connector.Record{
		{Source: "jira", Email: "bob@x.com", Name: "Bob", Topics: []string{"kafka"}},
	})
	// Slack arrives, then is re-read with different content.
	ix.Add([]connector.Record{{Source: "slack", Email: "cy@x.com", Name: "Cy", Text: "latency"}})
	ix.Add([]connector.Record{{Source: "slack", Email: "cy@x.com", Name: "Cy", Text: "throughput"}})

	for _, q := range []string{"billing", "kafka", "throughput"} {
		if got := ix.Search(q, 1); len(got) != 1 {
			t.Errorf("query %q returned %d results, want the untouched source kept", q, len(got))
		}
	}
	// The replaced Slack content is gone rather than lingering beside the new.
	if got := ix.Search("latency", 1); len(got) != 0 {
		t.Errorf("replaced source content survived: %v", got)
	}
}

// TestLoadNamesADamagedIndex checks a file that exists but will not parse comes
// back as ErrDamaged rather than a bare decoder complaint. It is the one failure
// where somebody cannot tell whether their data is gone, so the caller has to be
// able to recognize it and say what to do.
func TestLoadNamesADamagedIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, []byte("this is not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("Load = %v, want it to report a damaged index", err)
	}
	if !strings.Contains(err.Error(), "index.json") {
		t.Errorf("error = %q, want it to name the file", err)
	}
	// A missing file is a different problem with different advice, and the two
	// must not collapse into one message.
	if _, err := Load(filepath.Join(dir, "gone.json")); errors.Is(err, ErrDamaged) {
		t.Errorf("a missing index reported as damaged: %v", err)
	}
}
