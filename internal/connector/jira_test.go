package connector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/jira"
)

// TestJQLUsesUserTimezone verifies the incremental clause renders the watermark
// in the timezone JQL will be read in. An instant late on March 4 UTC is the
// morning of March 5 on Kiritimati, and formatting it any other way would shift
// the incremental window by the whole offset.
func TestJQLUsesUserTimezone(t *testing.T) {
	t.Parallel()
	loc, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	since := time.Date(2026, 3, 4, 20, 0, 0, 0, time.UTC)
	j := &Jira{opts: JiraOptions{Since: since}.withDefaults()}
	got := j.jql(loc)
	if want := `updated >= "2026/03/05 09:58"`; !strings.Contains(got, want) {
		t.Errorf("jql = %q, want it to contain %q", got, want)
	}
	// A nil location keeps the old rendering.
	if got := j.jql(nil); !strings.Contains(got, `updated >= "2026/03/04 19:58"`) {
		t.Errorf("jql(nil) = %q, want the unconverted wall clock", got)
	}
}

// TestJiraFetch verifies the assignee and reporter get topics from components,
// labels, summary words, and project name, with email and account-id identity.
func TestJiraFetch(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"total":2,"startAt":0,"issues":[`+
			`{"key":"SEC-1","fields":{"summary":"Fix wiz scan flaky",`+
			`"assignee":{"accountId":"a1","displayName":"Jane Roe","emailAddress":"jane@x.com"},`+
			`"reporter":{"accountId":"p1","displayName":"Pat","emailAddress":"pat@x.com"},`+
			`"components":[{"name":"scanning"}],"labels":["wiz"],`+
			`"project":{"key":"SEC","name":"Security"},"updated":"2026-06-20T09:30:00.000-0500"}},`+
			`{"key":"OPS-2","fields":{"summary":"Dashboard down",`+
			`"assignee":{"accountId":"b1","displayName":"Bob"},`+
			`"labels":["dashboard"],"project":{"key":"OPS","name":"Operations"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	client := jira.New(srv.URL, "me@x.com", "token")
	recs, err := NewJiraWithClient(client, JiraOptions{Projects: []string{"SEC"}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// A record carries both the source's own id and any email, so key by the
	// email when there is one: that is the identity the index settles on.
	byKey := make(map[string]Record)
	for _, r := range recs {
		key := r.Email
		if key == "" {
			key = r.PersonID
		}
		byKey[key] = r
	}

	// Topics carries what an issue stated as a label or component; everything
	// mined from a summary or description arrives as a weak topic. Both still
	// count toward affinity, so check the union for presence and the split for
	// which side each one landed on.
	jane := byKey["jane@x.com"]
	janeAll := append(append([]string(nil), jane.Topics...), jane.WeakTopics...)
	if !slices.Contains(janeAll, "wiz") ||
		!slices.Contains(janeAll, "scan") || !slices.Contains(janeAll, "scanning") {
		t.Errorf("jane topics = %v, weak = %v, want wiz, scan, scanning",
			jane.Topics, jane.WeakTopics)
	}
	// The wiz label and the scanning component were stated by the issue, so both
	// are curated; scan is only a summary word, so it stays weak.
	if !slices.Contains(jane.Topics, "wiz") || !slices.Contains(jane.Topics, "scanning") {
		t.Errorf("jane curated topics = %v, want the wiz label and scanning component", jane.Topics)
	}
	if !slices.Contains(jane.WeakTopics, "scan") {
		t.Errorf("jane weak topics = %v, want the summary word scan", jane.WeakTopics)
	}
	bob := byKey["jira:b1"]
	bobAll := append(append([]string(nil), bob.Topics...), bob.WeakTopics...)
	if !slices.Contains(bobAll, "dashboard") {
		t.Errorf("bob topics = %v, weak = %v, want dashboard", bob.Topics, bob.WeakTopics)
	}
	zone := time.FixedZone("", -5*3600)
	if want := time.Date(2026, 6, 20, 9, 30, 0, 0, zone); !byKey["jane@x.com"].Time.Equal(want) {
		t.Errorf("jane time = %v, want the issue update time %v", byKey["jane@x.com"].Time, want)
	}
	if !byKey["jira:b1"].Time.IsZero() {
		t.Errorf("bob time = %v, want zero for an issue without a date", byKey["jira:b1"].Time)
	}
}

// TestJiraSelfFiledIssueCountsOnce pins the double-count fix: a ticket
// somebody filed and assigned to themselves bumped every token twice, once as
// assignee and once as reporter, doubling their weight against colleagues
// whose tickets were filed by others.
func TestJiraSelfFiledIssueCountsOnce(t *testing.T) {
	t.Parallel()
	self := `{"accountId":"a1","displayName":"Jane Roe","emailAddress":"jane@x.com"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"total":2,"startAt":0,"issues":[`+
			`{"key":"SEC-1","fields":{"summary":"self filed",`+
			`"assignee":`+self+`,"reporter":`+self+`,`+
			`"labels":["wiz"],"project":{"key":"SEC","name":"Security"}}},`+
			`{"key":"SEC-2","fields":{"summary":"filed for bob",`+
			`"assignee":{"accountId":"b1","displayName":"Bob"},`+
			`"reporter":{"accountId":"p1","displayName":"Pat","emailAddress":"pat@x.com"},`+
			`"labels":["wiz"],"project":{"key":"SEC","name":"Security"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	client := jira.New(srv.URL, "me@x.com", "token")
	recs, err := NewJiraWithClient(client, JiraOptions{}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	counts := map[string]int{}
	for _, r := range recs {
		key := r.Email
		if key == "" {
			key = r.PersonID
		}
		counts[key] = count(r.Topics, "wiz")
	}
	// Jane held both roles on one issue: one credit. Bob and Pat held one role
	// each on the other: one credit each, so the distinct-role path still pays.
	if counts["jane@x.com"] != 1 {
		t.Errorf("jane wiz count = %d, want 1: assignee and reporter of one issue is one person", counts["jane@x.com"])
	}
	if counts["jira:b1"] != 1 || counts["pat@x.com"] != 1 {
		t.Errorf("bob = %d, pat = %d, want 1 each from their separate roles",
			counts["jira:b1"], counts["pat@x.com"])
	}
}

// TestJiraWarnsWhenTheCapTruncates verifies a capped search is indexed as
// partial and reported with the exact remainder and the flag that raises it,
// where it used to log the read as if complete. Git, Slack, and PagerDuty
// already warned on exactly this.
func TestJiraWarnsWhenTheCapTruncates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("startAt") != "" && r.URL.Query().Get("startAt") != "0" {
			io.WriteString(w, `{"total":3,"startAt":1,"issues":[]}`)
			return
		}
		io.WriteString(w, `{"total":3,"startAt":0,"issues":[`+
			`{"key":"OPS-1","fields":{"summary":"first",`+
			`"assignee":{"name":"jdoe","displayName":"Jane"},`+
			`"labels":["kafka"],"project":{"key":"OPS","name":"Ops"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	var log strings.Builder
	client := jira.NewServer(srv.URL, "")
	recs, err := NewJiraWithClient(client, JiraOptions{MaxIssues: 1, Log: &log}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: a truncated read must still index what it got: %v", err)
	}
	if len(recs) == 0 {
		t.Error("no records from the issues that were read")
	}
	for _, want := range []string{"1 of 3", "--max-issues", "invisible"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q; log:\n%s", want, log.String())
		}
	}
}

// TestJiraTime verifies the Jira colon-less zone form and RFC 3339 variants all
// parse, and that an unparseable string yields the zero time.
func TestJiraTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       string
		WantZero bool
	}{
		{In: "2026-07-05T12:34:56.789-0500"},
		{In: "2026-07-05T12:34:56Z"},
		{In: "2026-07-05T12:34:56.789Z"},
		{In: "2026-07-05T12:34:56+05:00"},
		{In: "not a time", WantZero: true},
		{In: "", WantZero: true},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := jiraTime(test.In).IsZero(); got != test.WantZero {
				t.Errorf("jiraTime(%q) zero=%v, want %v", test.In, got, test.WantZero)
			}
		})
	}
}

// TestJiraJQL verifies the query an incremental read builds: it restricts to
// issues updated since the watermark, orders them oldest first, and leaves an
// explicit JQL untouched.
func TestJiraJQL(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		Name      string
		Opts      JiraOptions
		WantHas   []string
		WantLacks []string
	}{{ // Test 0: Incremental with a project scope.
		Name: "incremental with projects", Opts: JiraOptions{Projects: []string{"SEC"}, Since: since},
		WantHas: []string{`project in ("SEC")`, `updated >= "2026/03/04 09:28"`, "ORDER BY updated ASC"},
	}, { // Test 1: Incremental with no scope.
		Name: "incremental no projects", Opts: JiraOptions{Since: since},
		WantHas: []string{`updated >= "2026/03/04 09:28"`, "ORDER BY updated ASC"}, WantLacks: []string{"project in"},
	}, { // Test 2: A full read keeps newest-first and adds no since clause.
		Name: "full with projects", Opts: JiraOptions{Projects: []string{"SEC"}},
		WantHas: []string{`project in ("SEC")`, "ORDER BY updated DESC"}, WantLacks: []string{"updated >="},
	}, { // Test 3: An explicit JQL is authoritative and ignores Since.
		Name: "explicit jql", Opts: JiraOptions{JQL: "assignee = currentUser() ORDER BY created DESC", Since: since},
		WantHas: []string{"assignee = currentUser() ORDER BY created DESC"}, WantLacks: []string{"updated >=", "ASC"},
	}}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			j := &Jira{opts: test.Opts.withDefaults()}
			got := j.jql(nil)
			for _, s := range test.WantHas {
				if !strings.Contains(got, s) {
					t.Errorf("jql = %q, want it to contain %q", got, s)
				}
			}
			for _, s := range test.WantLacks {
				if strings.Contains(got, s) {
					t.Errorf("jql = %q, want it to NOT contain %q", got, s)
				}
			}
		})
	}
}

// TestJiraFindsSubjectsWorkedTogether verifies that issues naming two subjects
// at once tie them to each other and name whoever resolved them.
func TestJiraFindsSubjectsWorkedTogether(t *testing.T) {
	t.Parallel()
	var issues []string
	for i := range 6 {
		issues = append(issues, fmt.Sprintf(
			`{"key":"BIL-%d","fields":{"summary":"reconcile %d",`+
				`"assignee":{"accountId":"a1","displayName":"Jane","emailAddress":"jane@x.com"},`+
				`"components":[{"name":"ledger"}],"labels":["billing"],`+
				`"project":{"key":"BIL","name":"Billing"}}}`, i, i))
	}
	for i := range 8 {
		issues = append(issues, fmt.Sprintf(
			`{"key":"SRCH-%d","fields":{"summary":"unrelated %d",`+
				`"assignee":{"accountId":"b1","displayName":"Bob","emailAddress":"bob@x.com"},`+
				`"components":[{"name":"indexing"}],"labels":["search"],`+
				`"project":{"key":"SRCH","name":"Search"}}}`, i, i))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"total":14,"startAt":0,"issues":[`+strings.Join(issues, ",")+`]}`)
	}))
	t.Cleanup(srv.Close)

	client := jira.New(srv.URL, "me@x.com", "token")
	recs, err := NewJiraWithClient(client, JiraOptions{Projects: []string{"BIL"}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	found := false
	for _, r := range recs {
		if r.Kind != KindTopic || r.Name != "billing" {
			continue
		}
		if r.Source != "jira" {
			t.Errorf("source = %q, want jira", r.Source)
		}
		for _, l := range r.Links {
			if l.To == "ledger" {
				found = true
				if l.Witnesses != 1 {
					t.Errorf("billing to ledger: %d people, want the one who resolved them", l.Witnesses)
				}
			}
			if l.To == "search" {
				t.Error("billing was tied to search, which no issue named alongside it")
			}
		}
	}
	if !found {
		t.Error("no issue tied billing to ledger, though six named both")
	}
}

// TestJiraJQLTimeKeepsTheSiteZone checks the incremental boundary is written in
// the zone the site itself used, not in UTC and not in the local zone.
//
// JQL accepts no timezone. The only reason an incremental read is correct is
// that the cursor still carries the offset the site wrote, so formatting it
// reproduces the site's own wall clock. Normalizing the cursor to UTC anywhere
// on the way here silently moves the boundary by the offset: measured against a
// live server, the same instant written +05:30 instead of Z pushed the boundary
// five and a half hours into the future and returned nothing, which would skip
// every issue in that window and then advance past them for good.
func TestJiraJQLTimeKeepsTheSiteZone(t *testing.T) {
	t.Parallel()
	// One instant, as three different sites would render it.
	instant := time.Date(2026, 8, 25, 19, 18, 2, 0, time.UTC)
	tests := []struct {
		Zone     *time.Location
		WantWall string
	}{
		{time.UTC, "2026/08/25 19:16"},
		{time.FixedZone("CDT", -5*3600), "2026/08/25 14:16"},
		{time.FixedZone("IST", 5*3600+1800), "2026/08/26 00:46"},
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := jiraJQLTime(instant.In(test.Zone))
			if got != test.WantWall {
				t.Errorf("jiraJQLTime = %q, want %q: the boundary must read as the "+
					"site's own wall clock, since JQL has no timezone", got, test.WantWall)
			}
			// The same instant in UTC must NOT produce the same string unless the
			// site is itself UTC. If it does, the zone has been thrown away.
			utc := jiraJQLTime(instant)
			if test.Zone != time.UTC && got == utc {
				t.Error("the zone was discarded: every site would get the UTC boundary")
			}
		})
	}
}

// TestJiraComponentIsAStatedSubject checks a component arrives as a subject the
// tracker declared, whole, and not as prose somebody happened to write.
//
// Components used to reach the index only as their separate words, which broke
// three things at once. The compound was lost, so "Structured Streaming" became
// two unrelated words. A component that tokenizes to nothing, like "SQL",
// vanished entirely. And because the stated list holds full names while only
// words arrived, no component ever matched it, so every component in a tracker
// was filed as mined prose: excluded from risk, from ownership, and from every
// connection between subjects. On a real tracker this fix recovered 95 subjects
// and doubled the connections found.
func TestJiraComponentIsAStatedSubject(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"total":1,"startAt":0,"issues":[`+
			`{"key":"F-1","fields":{"summary":"a fix",`+
			`"assignee":{"accountId":"a1","displayName":"Jane","emailAddress":"jane@x.com"},`+
			`"components":[{"name":"Build System / CI"},{"name":"SQL"}],"labels":[],`+
			`"project":{"key":"F","name":"Flink"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	client := jira.New(srv.URL, "me@x.com", "token")
	recs, err := NewJiraWithClient(client, JiraOptions{Projects: []string{"F"}}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var rec Record
	for _, r := range recs {
		if r.Email == "jane@x.com" {
			rec = r
		}
	}
	if rec.Email == "" {
		t.Fatal("the assignee did not come back at all")
	}
	// Subjects are lowercased on the way in, like every other token.
	for _, want := range []string{"build system / ci", "sql"} {
		if !slices.Contains(rec.Topics, want) {
			t.Errorf("%q is not among the stated subjects %v: a component the tracker "+
				"declares must not be filed as prose", want, rec.Topics)
		}
	}
	// The words still come through, so a question asked in one of them matches.
	all := append(append([]string{}, rec.Topics...), rec.WeakTopics...)
	if !slices.Contains(all, "system") {
		t.Errorf("the words of a component were dropped; got %v", all)
	}
}
