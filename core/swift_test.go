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
	calls := []struct {
		Name string
		Call func() (string, error)
	}{
		{"ask", func() (string, error) { return h.Ask("terraform", 3) }},
		{"search", func() (string, error) { return h.Search("angela", 3) }},
		{"person", func() (string, error) { return h.Person("angela@corp.com") }},
		{"recall", func() (string, error) { return h.Recall("angela@corp.com", "billing retries", 2) }},
		{"status", func() (string, error) { return h.Status(h.current().eng.Index().BuiltAt().Unix() + 3600) }},
		{"index", func() (string, error) { return w.Index("git", flags, now.Unix()) }},
		{"refresh", func() (string, error) { return w.Refresh("", now.Add(time.Hour).Unix()) }},
		{"changes", func() (string, error) { return f.ChangesSince(0) }},
		{"wipe", func() (string, error) { return Wipe(fresh) }},
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
