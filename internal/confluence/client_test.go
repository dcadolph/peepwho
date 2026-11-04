package confluence

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSearchCQLStopsARunawayServer verifies an uncapped search cannot be held
// in a loop by a server that keeps promising another page: the read stops at
// the request bound and reports itself truncated with what it gathered.
func TestSearchCQLStopsARunawayServer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[{"title":"again"}],"size":1,"limit":100,`+
			`"_links":{"next":"/rest/api/content/search?start=next"}}`)
	}))
	t.Cleanup(srv.Close)

	pages, err := NewServer(srv.URL, "").Pages(context.Background(), Query{CQL: "type=page"})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Pages err = %v, want ErrTruncated at the request bound", err)
	}
	if len(pages) == 0 {
		t.Error("the pages read before the bound were not returned")
	}
}

// TestBuildCQL verifies the query an incremental read builds: it restricts to
// pages modified since the watermark, orders them oldest first, and leaves an
// explicit CQL untouched.
func TestBuildCQL(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 3, 4, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		Name      string
		Q         Query
		WantHas   []string
		WantLacks []string
	}{{ // Test 0: Incremental adds the modified-since clause and oldest-first order.
		Name: "incremental with spaces", Q: Query{Spaces: []string{"ENG"}, Since: since},
		WantHas: []string{`space in ("ENG")`, `lastmodified >= "2026/03/04 09:28"`, "order by lastmodified asc"},
	}, { // Test 1: A full read has no time clause.
		Name: "full with spaces", Q: Query{Spaces: []string{"ENG"}},
		WantHas: []string{`space in ("ENG")`}, WantLacks: []string{"lastmodified"},
	}, { // Test 2: An explicit CQL is authoritative and ignores Since.
		Name: "explicit cql", Q: Query{CQL: "label = runbook", Since: since},
		WantHas: []string{"label = runbook"}, WantLacks: []string{"lastmodified", "order by"},
	}}
	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			got := buildCQL(test.Q)
			for _, s := range test.WantHas {
				if !strings.Contains(got, s) {
					t.Errorf("buildCQL = %q, want it to contain %q", got, s)
				}
			}
			for _, s := range test.WantLacks {
				if strings.Contains(got, s) {
					t.Errorf("buildCQL = %q, want it to NOT contain %q", got, s)
				}
			}
		})
	}
}

// TestPages verifies the Cloud v2 read reassembles a page from the separate
// pages, spaces, labels, and user endpoints, since v2 returns account ids only
// and serves the space name and labels apart from the page.
func TestPages(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/api/v2/spaces", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[{"id":"100","key":"SEC","name":"Security"}]}`)
	})
	mux.HandleFunc("/wiki/api/v2/pages", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[{"id":"7","title":"Wiz scanning runbook","spaceId":"100",`+
			`"authorId":"a1","createdAt":"2026-06-20T09:30:00.000Z",`+
			`"version":{"authorId":"a2","createdAt":"2026-06-21T09:30:00.000Z"}}],"_links":{}}`)
	})
	mux.HandleFunc("/wiki/api/v2/pages/7/labels", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[{"name":"wiz"}]}`)
	})
	mux.HandleFunc("/wiki/rest/api/user", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accountId") == "a1" {
			io.WriteString(w, `{"accountId":"a1","displayName":"Jane","email":"jane@x.com"}`)
			return
		}
		io.WriteString(w, `{"accountId":"a2","displayName":"Bob","email":"bob@x.com"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pages, err := New(srv.URL, "me@x.com", "token").Pages(context.Background(), Query{Spaces: []string{"SEC"}})
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	p := pages[0]
	if p.Title != "Wiz scanning runbook" || p.Space.Key != "SEC" || p.Space.Name != "Security" {
		t.Errorf("page = %+v", p)
	}
	if !slices.Contains(p.LabelNames(), "wiz") {
		t.Errorf("labels = %v, want wiz", p.LabelNames())
	}
	authors := p.Authors()
	if len(authors) != 2 || authors[0].Email != "jane@x.com" || authors[1].Email != "bob@x.com" {
		t.Errorf("authors = %+v, want jane the creator then bob the editor", authors)
	}
}

// TestNewEmptyPanics verifies the constructor guards empty arguments.
func TestNewEmptyPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New with empty args did not panic")
		}
	}()
	New("", "e", "t")
}

// TestUserIdentity verifies the identity fallback: Cloud keys on the account
// id, Server and Data Center on the username or user key.
func TestUserIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   User
		Want string
	}{
		{User{AccountID: "a1", Username: "jdoe", UserKey: "k1"}, "a1"}, // Test 0: Cloud.
		{User{Username: "jdoe", UserKey: "k1"}, "jdoe"},                // Test 1: Server username.
		{User{UserKey: "k1"}, "k1"},                                    // Test 2: Server key.
		{User{}, ""},                                                   // Test 3: none.
	}
	for testNum, test := range tests {
		if got := test.In.Identity(); got != test.Want {
			t.Errorf("test %d: Identity() = %q, want %q", testNum, got, test.Want)
		}
	}
}

// TestNewServerAnonymousAtRoot verifies the Server client serves the REST API
// at the site root without the Cloud /wiki prefix, sends no auth header when
// the token is empty, and reads Server-shaped users. This is the shape a public
// wiki such as Apache's Confluence returns.
func TestNewServerAnonymousAtRoot(t *testing.T) {
	t.Parallel()
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"size":1,"limit":100,"results":[{"title":"KRaft design",`+
			`"space":{"key":"KAFKA","name":"Kafka"},`+
			`"history":{"createdBy":{"username":"showuon","displayName":"Luke Chen"}},`+
			`"version":{"by":{"username":"showuon","displayName":"Luke Chen"}}}]}`)
	}))
	t.Cleanup(srv.Close)

	c := NewServer(srv.URL, "")
	pages, err := c.Pages(context.Background(), Query{CQL: "type=page", Max: 10})
	if err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if strings.HasPrefix(gotPath, "/wiki/") {
		t.Errorf("Server hit the Cloud /wiki path %q", gotPath)
	}
	if !strings.HasPrefix(gotPath, apiBaseServer) {
		t.Errorf("path = %q, want the Server API base %q", gotPath, apiBaseServer)
	}
	if gotAuth != "" {
		t.Errorf("anonymous client sent an Authorization header: %q", gotAuth)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	if got := pages[0].History.CreatedBy.Identity(); got != "showuon" {
		t.Errorf("creator identity = %q, want the username showuon", got)
	}
}

// TestNewServerBearerToken verifies a Confluence PAT is sent as a bearer.
func TestNewServerBearerToken(t *testing.T) {
	t.Parallel()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"size":0,"limit":100,"results":[]}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := NewServer(srv.URL, "PAT9").Pages(context.Background(), Query{Max: 10}); err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if gotAuth != "Bearer PAT9" {
		t.Errorf("Authorization = %q, want Bearer PAT9", gotAuth)
	}
}

// TestSearchCQLPermissionFilteredPages verifies pagination survives the pages
// permission filtering shortens: a short page in the middle of the results,
// with a next link present, must not end the read, and the window advances by
// what was requested rather than by the filtered size. Server and Data Center
// hit this on any instance with restricted spaces.
func TestSearchCQLPermissionFilteredPages(t *testing.T) {
	t.Parallel()
	var starts []string
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/content/search", func(w http.ResponseWriter, r *http.Request) {
		start := r.URL.Query().Get("start")
		starts = append(starts, start)
		switch start {
		case "0":
			// 100 requested, 1 visible after filtering, more exist.
			io.WriteString(w, `{"results":[{"id":"1","title":"visible one"}],`+
				`"size":1,"limit":100,"_links":{"next":"/rest/api/content/search?start=100"}}`)
		case "100":
			// Empty window entirely filtered away, but still not the end.
			io.WriteString(w, `{"results":[],"size":0,"limit":100,`+
				`"_links":{"next":"/rest/api/content/search?start=200"}}`)
		default:
			// The real last page: short and no next link.
			io.WriteString(w, `{"results":[{"id":"3","title":"visible two"}],`+
				`"size":1,"limit":100,"_links":{}}`)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pages, err := NewServer(srv.URL, "token").searchCQL(context.Background(), "type = page", 0)
	if err != nil {
		t.Fatalf("searchCQL: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want both visible pages despite the filtered windows", len(pages))
	}
	if want := []string{"0", "100", "200"}; !slices.Equal(starts, want) {
		t.Errorf("start params = %v, want %v: the window advances by the requested limit", starts, want)
	}
}

// TestSearchCQLLegacyShortPageStops verifies the old behavior still terminates
// on servers that omit the next link: a short page with no link is the end.
func TestSearchCQLLegacyShortPageStops(t *testing.T) {
	t.Parallel()
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/content/search", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		io.WriteString(w, `{"results":[{"id":"1","title":"only"}],"size":1,"limit":100}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	pages, err := NewServer(srv.URL, "token").searchCQL(context.Background(), "type = page", 0)
	if err != nil {
		t.Fatalf("searchCQL: %v", err)
	}
	if len(pages) != 1 || calls != 1 {
		t.Errorf("pages = %d after %d calls, want one page from one call", len(pages), calls)
	}
}

// TestSearchCQLUsesUserTimezone verifies an incremental read renders its
// watermark in the timezone the server reads CQL dates in, learned from the
// current-user endpoint. Three in the morning UTC on January 15 is the evening
// of January 14 in Chicago; formatting it any other way shifts the incremental
// window by the whole offset.
func TestSearchCQLUsesUserTimezone(t *testing.T) {
	t.Parallel()
	var cqls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/user/current", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"username":"doug","timeZone":"America/Chicago"}`)
	})
	mux.HandleFunc("/rest/api/content/search", func(w http.ResponseWriter, r *http.Request) {
		cqls = append(cqls, r.URL.Query().Get("cql"))
		io.WriteString(w, `{"results":[],"size":0,"limit":100,"_links":{}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	since := time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC)
	if _, err := NewServer(srv.URL, "token").Pages(context.Background(), Query{Since: since}); err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if len(cqls) == 0 || !strings.Contains(cqls[0], `lastmodified >= "2026/01/14 20:58"`) {
		t.Errorf("cql = %q, want the watermark rendered in America/Chicago", cqls)
	}
}

// TestSearchCQLTimezoneFallback verifies a server that reports no timezone
// leaves the watermark exactly as it was given, the old behavior.
func TestSearchCQLTimezoneFallback(t *testing.T) {
	t.Parallel()
	var cqls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/user/current", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"username":"doug"}`)
	})
	mux.HandleFunc("/rest/api/content/search", func(w http.ResponseWriter, r *http.Request) {
		cqls = append(cqls, r.URL.Query().Get("cql"))
		io.WriteString(w, `{"results":[],"size":0,"limit":100,"_links":{}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	since := time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC)
	if _, err := NewServer(srv.URL, "token").Pages(context.Background(), Query{Since: since}); err != nil {
		t.Fatalf("Pages: %v", err)
	}
	if len(cqls) == 0 || !strings.Contains(cqls[0], `lastmodified >= "2026/01/15 02:58"`) {
		t.Errorf("cql = %q, want the unconverted watermark", cqls)
	}
}
