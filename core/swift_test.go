package core

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
)

// updateSwift rewrites the fixtures the Swift decode tests read when set.
var updateSwift = flag.Bool("update-swift", false, "Rewrite the Swift decode fixtures.") //nolint:gochecknoglobals // Flag.

// swiftFixtureDir holds one answer per core call for the Mac app's decode tests.
const swiftFixtureDir = "testdata/swift"

// jsonShape reduces a decoded JSON value to its structure: object keys with the shape of each
// value, an array as the shape of its first element, and a leaf as its type name. Values are
// left out because the simulated company dates its history from the wall clock.
func jsonShape(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jsonShape(e)
		}
		return out
	case []any:
		if len(x) == 0 {
			return []any{}
		}
		return []any{jsonShape(x[0])}
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	default:
		return "null"
	}
}

// shapeOf decodes raw and returns its shape.
func shapeOf(t *testing.T, name, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("%s: unmarshal %s: %v", name, raw, err)
	}
	return jsonShape(v)
}

// TestSwiftFixtures verifies each core answer still has the shape of the fixture the Mac app's
// decode tests read, so renaming a field in Go fails here until the fixtures are rewritten with
// -update-swift, and then fails the Swift tests until the app's models follow.
func TestSwiftFixtures(t *testing.T) {
	t.Parallel()
	h, err := Open(newTestDataDir(t, nil), "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	built := h.current().eng.Index().BuiltAt()
	if err := h.store.SaveInvocations(engine.Invocations{
		"git": {"repo:/src/app": {
			Flags: []string{"--repo-path", "/src/app"}, RanAt: built, AttemptedAt: built.Add(30 * time.Minute),
			Error: "repository not found",
			Found: &engine.Outcome{Repos: 58, People: 1},
		}},
		"slack": {"": {}},
	}); err != nil {
		t.Fatalf("SaveInvocations: %v", err)
	}
	fresh := filepath.Join(t.TempDir(), "data")
	w, err := Open(fresh, "")
	if err != nil {
		t.Fatalf("Open fresh: %v", err)
	}
	flags := "--repo-path\n" + newGitRepo(t) + "\n--git-since-days=10000\n"
	folded := t.TempDir()
	base := "Jane Doe,jane@x.com,Engineer,Billing,retries\n"
	foldRosters(t, folded, nil, now, base, base+"Ann Lee,ann@x.com,Engineer,Payments,ledger\n",
		base+"Ann Lee,ann@x.com,Engineer,Billing,ledger\n")
	f, err := Open(folded, "")
	if err != nil {
		t.Fatalf("Open folded: %v", err)
	}
	sample := filepath.Join(t.TempDir(), "sample")
	if err := os.MkdirAll(sample, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	copySample(t, h.store.Dir(), sample)
	sampled, err := Open(filepath.Join(t.TempDir(), "sampled"), "")
	if err != nil {
		t.Fatalf("Open sampled: %v", err)
	}
	teams, err := Open(newTeamsDataDir(t), "")
	if err != nil {
		t.Fatalf("Open teams: %v", err)
	}
	departed, err := Open(newDepartedDataDir(t), "")
	if err != nil {
		t.Fatalf("Open departed: %v", err)
	}
	calls := []struct {
		Name string
		Call func() (string, error)
	}{
		{"ask", func() (string, error) { return h.Ask("terraform", 3) }},
		{"ask_teams", func() (string, error) { return teams.Ask("contracts", 3) }},
		{"ask_departed", func() (string, error) { return departed.Ask("ledgers", 3) }},
		{"person_departed", func() (string, error) { return departed.Person("lena@example.com") }},
		{"people_departed", func() (string, error) { return departed.People(0) }},
		{"status_departed", func() (string, error) { return departed.Status(now.Unix()) }},
		{"search", func() (string, error) { return h.Search("angela", 3) }},
		{"person", func() (string, error) { return h.Person("angela@example.com") }},
		{"people", func() (string, error) { return h.People(3) }},
		{"recall", func() (string, error) { return h.Recall("angela@example.com", "billing retries", 2) }},
		{"status", func() (string, error) { return h.Status(built.Unix() + 3600) }},
		{"index", func() (string, error) { return w.Index("git", flags, now.Unix()) }},
		{"refresh", func() (string, error) { return w.Refresh("", now.Add(time.Hour).Unix()) }},
		{"changes", func() (string, error) { return f.ChangesSince(0) }},
		{"wipe", func() (string, error) { return Wipe(fresh) }},
		{"import_sample", func() (string, error) { return sampled.ImportSample(sample, now.Unix()) }},
		{"status_sample", func() (string, error) { return sampled.Status(now.Unix() + 180) }},
	}
	// The calls run in order because index, refresh, and wipe share one directory.
	for testNum, c := range calls {
		raw, err := c.Call()
		if err != nil {
			t.Fatalf("test %d %s: %v", testNum, c.Name, err)
		}
		path := filepath.Join(swiftFixtureDir, c.Name+".json")
		if *updateSwift {
			// The machine's temp folder is replaced so a fixture never names the machine.
			raw = strings.ReplaceAll(raw, filepath.Clean(os.TempDir()), "/tmp")
			var v any
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatalf("test %d %s: %v", testNum, c.Name, err)
			}
			pretty, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				t.Fatalf("test %d %s: %v", testNum, c.Name, err)
			}
			if err := os.WriteFile(path, append(pretty, '\n'), 0o600); err != nil {
				t.Fatalf("test %d %s: %v", testNum, c.Name, err)
			}
			continue
		}
		stored, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("test %d %s: %v (run go test ./core -run TestSwiftFixtures -update-swift)", testNum, c.Name, err)
		}
		want, got := shapeOf(t, path, string(stored)), shapeOf(t, c.Name, raw)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("test %d %s: shape changed, rewrite with -update-swift and update the app (-want +got):\n%s",
				testNum, c.Name, diff)
		}
	}
}

// newTeamsDataDir saves an index of one person and one CODEOWNERS team that own the same
// subject, so an answer carries both people and teams of record.
func newTeamsDataDir(t *testing.T) string {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Name: "Carla Counsel", Email: "carla@example.com",
			Topics: []string{"contracts"}, Source: "git"},
		{Kind: connector.KindPerson, Source: "codeowners", Weight: 1, Name: "@acme/legal-squad",
			PersonID: "codeowners:acme/legal-squad", Topics: []string{"contracts"}},
	})
	ix.Canonicalize()
	dir := t.TempDir()
	if err := engine.NewStore(engine.Dir(dir), nil).SaveIndex(ix); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	return dir
}

// newDepartedDataDir writes an index holding one active and one deactivated
// person who both know ledgers, with a Slack link each, so the departed fixtures
// carry the deactivated flag everywhere the app reads it.
func newDepartedDataDir(t *testing.T) string {
	t.Helper()
	ix := index.New()
	ix.Build([]connector.Record{
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U1", Name: "Nora Here",
			Email: "nora@example.com", Title: "Engineer", Topics: []string{"ledgers"},
			Link: "slack://user?team=T1&id=U1", Account: model.AccountActive},
		{Kind: connector.KindPerson, Source: "slack", PersonID: "slack:U2", Name: "Lena Gone",
			Email: "lena@example.com", Title: "Engineer", Topics: []string{"ledgers", "ledgers"},
			Link: "slack://user?team=T1&id=U2", Account: model.AccountDeactivated},
	})
	ix.Canonicalize()
	dir := t.TempDir()
	if err := engine.NewStore(engine.Dir(dir), nil).SaveIndex(ix); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	return dir
}

// copySample copies the plain index and conversations from src to dir, as the app bundles them.
func copySample(t *testing.T, src engine.Dir, dir string) {
	t.Helper()
	for _, path := range []string{src.Index(), src.Episodes()} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(path)), raw, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}
