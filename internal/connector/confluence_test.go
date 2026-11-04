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

	"github.com/dcadolph/peepwho/internal/confluence"
)

// TestConfluenceFetch verifies authors get topics from labels, title words, and
// space name, with email and account-id identity, reading through the Cloud v2
// endpoints that serve the space, labels, and identities apart from the page.
func TestConfluenceFetch(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/api/v2/pages", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[`+
			`{"id":"1","title":"Wiz scanning runbook","spaceId":"100","authorId":"a1",`+
			`"createdAt":"2026-06-24T09:00:00.000Z",`+
			`"version":{"authorId":"a1","createdAt":"2026-06-25T14:00:00.000Z"}},`+
			`{"id":"2","title":"Dashboard outage","spaceId":"200","authorId":"b1",`+
			`"createdAt":"2026-06-10T09:00:00.000Z",`+
			`"version":{"authorId":"b1","createdAt":"2026-06-10T09:00:00.000Z"}}`+
			`],"_links":{}}`)
	})
	mux.HandleFunc("/wiki/api/v2/spaces/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/100") {
			io.WriteString(w, `{"id":"100","key":"SEC","name":"Security"}`)
			return
		}
		io.WriteString(w, `{"id":"200","key":"OPS","name":"Operations"}`)
	})
	mux.HandleFunc("/wiki/api/v2/pages/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/wiki/api/v2/pages/1/") {
			io.WriteString(w, `{"results":[{"name":"wiz"}]}`)
			return
		}
		io.WriteString(w, `{"results":[{"name":"dashboard"}]}`)
	})
	mux.HandleFunc("/wiki/rest/api/user", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accountId") == "a1" {
			io.WriteString(w, `{"accountId":"a1","displayName":"Jane","email":"jane@x.com"}`)
			return
		}
		io.WriteString(w, `{"accountId":"b1","displayName":"Bob"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := confluence.New(srv.URL, "me@x.com", "token")
	recs, err := NewConfluenceWithClient(client, ConfluenceOptions{}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	byKey := make(map[string]Record)
	for _, r := range recs {
		key := r.PersonID
		if key == "" {
			key = r.Email
		}
		byKey[key] = r
	}

	// A page's labels are curated topics; its title, space, and body words are
	// weak. Both carry affinity, so check the union for presence.
	all := func(r Record) []string {
		return append(append([]string(nil), r.Topics...), r.WeakTopics...)
	}
	jane := byKey["jane@x.com"]
	if !slices.Contains(all(jane), "wiz") || !slices.Contains(all(jane), "scanning") {
		t.Errorf("jane topics = %v, weak = %v, want wiz, scanning", jane.Topics, jane.WeakTopics)
	}
	if !slices.Contains(jane.Topics, "wiz") {
		t.Errorf("jane curated topics = %v, want the wiz label", jane.Topics)
	}
	if !slices.Contains(jane.WeakTopics, "scanning") {
		t.Errorf("jane weak topics = %v, want the title word scanning", jane.WeakTopics)
	}
	if bob := byKey["confluence:b1"]; !slices.Contains(all(bob), "dashboard") {
		t.Errorf("bob topics = %v, weak = %v, want dashboard", bob.Topics, bob.WeakTopics)
	}
	if want := time.Date(2026, 6, 25, 14, 0, 0, 0, time.UTC); !byKey["jane@x.com"].Time.Equal(want) {
		t.Errorf("jane time = %v, want the page edit time %v", byKey["jane@x.com"].Time, want)
	}
}

// TestConfluenceWarnsWhenTheCapTruncates verifies a capped read is indexed as
// partial and reported with the flag that raises it, where it used to log the
// read as if complete. Git, Slack, and PagerDuty already warned on exactly
// this.
func TestConfluenceWarnsWhenTheCapTruncates(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/api/v2/pages", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[`+
			`{"id":"1","title":"Kept page","spaceId":"100","authorId":"a1",`+
			`"createdAt":"2026-06-24T09:00:00.000Z",`+
			`"version":{"authorId":"a1","createdAt":"2026-06-25T14:00:00.000Z"}},`+
			`{"id":"2","title":"Cut page","spaceId":"100","authorId":"a1",`+
			`"createdAt":"2026-06-10T09:00:00.000Z",`+
			`"version":{"authorId":"a1","createdAt":"2026-06-10T09:00:00.000Z"}}`+
			`],"_links":{"next":"/wiki/api/v2/pages?cursor=more"}}`)
	})
	mux.HandleFunc("/wiki/api/v2/spaces/", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"100","key":"ENG","name":"Engineering"}`)
	})
	mux.HandleFunc("/wiki/api/v2/pages/", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[]}`)
	})
	mux.HandleFunc("/wiki/rest/api/user", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"accountId":"a1","displayName":"Jane","email":"jane@x.com"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var log strings.Builder
	client := confluence.New(srv.URL, "me@x.com", "token")
	recs, err := NewConfluenceWithClient(client, ConfluenceOptions{MaxPages: 1, Log: &log}).
		Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: a truncated read must still index what it got: %v", err)
	}
	if len(recs) == 0 {
		t.Error("no records from the pages that were read")
	}
	for _, want := range []string{"--max-pages", "invisible"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q; log:\n%s", want, log.String())
		}
	}
}

// TestConfluenceFindsSubjectsWorkedTogether verifies that pages labeled with two
// subjects at once tie them to each other and name whoever last wrote them.
func TestConfluenceFindsSubjectsWorkedTogether(t *testing.T) {
	t.Parallel()
	var pages []string
	labels := make(map[string]string)
	for i := range 6 {
		id := fmt.Sprintf("b%d", i)
		pages = append(pages, fmt.Sprintf(
			`{"id":"%s","title":"reconcile %d","spaceId":"100","authorId":"a1",`+
				`"createdAt":"2026-06-24T09:00:00.000Z",`+
				`"version":{"authorId":"a1","createdAt":"2026-06-25T14:00:00.000Z"}}`, id, i))
		labels[id] = `{"results":[{"name":"billing"},{"name":"ledger"}]}`
	}
	for i := range 8 {
		id := fmt.Sprintf("s%d", i)
		pages = append(pages, fmt.Sprintf(
			`{"id":"%s","title":"unrelated %d","spaceId":"100","authorId":"b1",`+
				`"createdAt":"2026-06-10T09:00:00.000Z",`+
				`"version":{"authorId":"b1","createdAt":"2026-06-10T09:00:00.000Z"}}`, id, i))
		labels[id] = `{"results":[{"name":"search"},{"name":"indexing"}]}`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/api/v2/pages", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"results":[`+strings.Join(pages, ",")+`],"_links":{}}`)
	})
	mux.HandleFunc("/wiki/api/v2/spaces/", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"id":"100","key":"ENG","name":"Engineering"}`)
	})
	mux.HandleFunc("/wiki/api/v2/pages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/pages/"), "/labels")
		if body, ok := labels[id]; ok {
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, `{"results":[]}`)
	})
	mux.HandleFunc("/wiki/rest/api/user", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accountId") == "a1" {
			io.WriteString(w, `{"accountId":"a1","displayName":"Jane","email":"jane@x.com"}`)
			return
		}
		io.WriteString(w, `{"accountId":"b1","displayName":"Bob"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := confluence.New(srv.URL, "me@x.com", "token")
	recs, err := NewConfluenceWithClient(client, ConfluenceOptions{}).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	found := false
	for _, r := range recs {
		if r.Kind != KindTopic || r.Name != "billing" {
			continue
		}
		if r.Source != "confluence" {
			t.Errorf("source = %q, want confluence", r.Source)
		}
		for _, l := range r.Links {
			if l.To == "ledger" {
				found = true
				if l.Witnesses != 1 {
					t.Errorf("billing to ledger: %d people, want the one who wrote them", l.Witnesses)
				}
			}
			if l.To == "search" {
				t.Error("billing was tied to search, which no page named alongside it")
			}
		}
	}
	if !found {
		t.Error("no page tied billing to ledger, though six carried both labels")
	}
}
