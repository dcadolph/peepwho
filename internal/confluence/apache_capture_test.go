package confluence

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newApacheReplay serves the content search responses captured from Apache's
// public Confluence Server (cwiki.apache.org, space KAFKA, two windows of 25).
// Each capture is served at the start it was captured from and nothing is
// served anywhere else, as the real server does. The captured windows hold 25
// rows each while the client asks for more, which is itself real behavior:
// servers cap their windows below what clients request, so a client that
// advances by the window it asked for lands past the second capture and loses
// it. The next link, not the window size, says whether more results exist.
func newApacheReplay(t *testing.T) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("read capture: %v", err)
		}
		return data
	}
	page1, page2 := read("apache_search_page1.json"), read("apache_search_page2.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/content/search") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("start") {
		case "", "0":
			_, _ = w.Write(page1)
		case "25":
			_, _ = w.Write(page2)
		default:
			_, _ = w.Write([]byte(`{"results":[],"size":0,"limit":25,"_links":{}}`))
		}
	}))
}

// TestPagesApacheCapture runs the Server client over captured Apache
// Confluence responses and holds the parse to what the real wiki contains.
func TestPagesApacheCapture(t *testing.T) {
	t.Parallel()
	srv := newApacheReplay(t)
	t.Cleanup(srv.Close)

	c := NewServer(srv.URL, "")
	pages, err := c.Pages(context.Background(), Query{Spaces: []string{"KAFKA"}, Max: 50})
	// The captured wiki offers more results past these two pages, so a
	// 50-page read is truncated and the client must say so rather than
	// logging the read as complete.
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("Pages err = %v, want ErrTruncated: the capture offers more results", err)
	}
	if len(pages) != 50 {
		t.Fatalf("pages = %d, want both captured pages returned alongside the truncation", len(pages))
	}

	authors := make(map[string]bool)
	withBody, withCreator := 0, 0
	for _, p := range pages {
		if p.Title == "" {
			t.Error("a page has no title")
		}
		if p.Space.Key != "KAFKA" {
			t.Errorf("page %q space = %q", p.Title, p.Space.Key)
		}
		if p.Version.By == nil {
			t.Errorf("page %q has no last editor", p.Title)
		} else if p.Version.By.Identity() == "" {
			// Server users carry usernames and no emails; an empty identity
			// would credit the edit to nobody.
			t.Errorf("page %q editor %q has empty identity", p.Title, p.Version.By.DisplayName)
		}
		if p.Version.When.IsZero() {
			t.Errorf("page %q has no edit time", p.Title)
		}
		if p.Version.When.After(time.Now().Add(24 * time.Hour)) {
			t.Errorf("page %q edited in the future: %v", p.Title, p.Version.When)
		}
		for _, u := range p.Authors() {
			if u != nil && u.Identity() != "" {
				authors[u.Identity()] = true
			}
		}
		if p.History.CreatedBy != nil {
			withCreator++
		}
		if body := p.BodyText(); body != "" {
			withBody++
			// Storage format is XHTML with macros; the plain-text extraction
			// must not leak markup into what gets indexed and shown.
			if strings.Contains(body, "<ac:") || strings.Contains(body, "</p>") {
				t.Errorf("page %q body leaks markup: %.80q", p.Title, body)
			}
		}
	}
	// Properties of any 50 recently edited KAFKA wiki pages, generous enough
	// to survive a re-capture.
	if len(authors) < 10 {
		t.Errorf("distinct authors = %d, want a real spread of editors", len(authors))
	}
	if withBody < 40 {
		t.Errorf("pages with bodies = %d, want nearly all", withBody)
	}
	if withCreator < 40 {
		t.Errorf("pages with creators = %d, want nearly all", withCreator)
	}
}
