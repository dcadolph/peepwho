package pagerduty

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newSchemaReplay serves fixtures built to PagerDuty's documented response
// schema, including the shapes that trip naive parsers: assignments embed
// full user objects because the client asks include[]=assignees, while
// acknowledgements and last_status_change_by stay bare references with a
// summary and no name or email, and an auto-resolved incident's last status
// change is a service_reference, not a person. No public PagerDuty instance
// exists to capture from; replace these with a live capture when an account
// with a token is available.
func newSchemaReplay(t *testing.T) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		return data
	}
	services := read("pd_services.json")
	oncalls := read("pd_oncalls.json")
	incidents := read("pd_incidents.json")
	notes := read("pd_notes.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/services"):
			_, _ = w.Write(services)
		case strings.HasSuffix(r.URL.Path, "/oncalls"):
			_, _ = w.Write(oncalls)
		case strings.HasSuffix(r.URL.Path, "/incidents"):
			_, _ = w.Write(incidents)
		case strings.HasSuffix(r.URL.Path, "/notes"):
			_, _ = w.Write(notes)
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestSchemaCapture runs the client over schema-faithful responses and holds
// the parse to the shapes the real API nests.
func TestSchemaCapture(t *testing.T) {
	t.Parallel()
	srv := newSchemaReplay(t)
	t.Cleanup(srv.Close)
	c := New("test-token", WithBaseURL(srv.URL))
	ctx := context.Background()

	services, err := c.Services(ctx)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(services) != 2 || services[0].EscalationPolicy.ID != "PPOL0001" {
		t.Errorf("services = %+v, want both with policies", services)
	}

	oncalls, err := c.OnCalls(ctx)
	if err != nil {
		t.Fatalf("OnCalls: %v", err)
	}
	if len(oncalls) != 2 {
		t.Fatalf("oncalls = %d", len(oncalls))
	}
	if u := oncalls[0].User; u.Email != "alice@corp.com" || u.Name != "Alice Nguyen" {
		t.Errorf("oncall user = %+v, want the full embedded user", u)
	}

	incidents, err := c.Incidents(ctx, time.Now().AddDate(-1, 0, 0), 0)
	if err != nil {
		t.Fatalf("Incidents: %v", err)
	}
	if len(incidents) != 2 {
		t.Fatalf("incidents = %d", len(incidents))
	}

	human, auto := incidents[0], incidents[1]
	if !human.Resolved() || !auto.Resolved() {
		t.Error("both fixture incidents are resolved")
	}
	people := human.People()
	if len(people) != 2 {
		t.Fatalf("human incident people = %+v, want alice (assigned and resolving, once) and bob",
			people)
	}
	ids := map[string]bool{}
	for _, u := range people {
		ids[u.ID] = true
	}
	if !ids["PUSR0001"] || !ids["PUSR0002"] {
		t.Errorf("people ids = %v, want the assignee and the acknowledger", ids)
	}
	// Alice appears as a full user (include[]=assignees) and again as the
	// resolving user_reference; both are one person keyed by id.
	if people[0].ID != "PUSR0001" || people[0].Email != "alice@corp.com" {
		t.Errorf("assignee = %+v, want the full user object with email kept", people[0])
	}

	// The auto-resolved incident was settled by the service itself; a
	// service_reference counted as a person would credit expertise to a
	// monitoring integration.
	if got := auto.People(); len(got) != 0 {
		t.Errorf("auto-resolved incident people = %+v, want nobody", got)
	}

	notes, err := c.IncidentNotes(ctx, "PINC0001")
	if err != nil {
		t.Fatalf("IncidentNotes: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Content, "event id") {
		t.Errorf("notes = %+v, want the resolution note", notes)
	}
}
