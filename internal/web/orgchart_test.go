package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/resolve"
)

// noAsk is a stub Ask that satisfies the required Config field.
func noAsk(context.Context, string, string, string, int) (resolve.Answer, error) {
	return resolve.Answer{}, nil
}

// TestOrgchartRouteServesPage confirms /orgchart returns the HTML page when a
// directory is configured.
func TestOrgchartRouteServesPage(t *testing.T) {
	t.Parallel()
	dir := resolve.Directory{People: []resolve.DirectoryPerson{{ID: "a", Name: "Ada"}}}
	h, err := Handler(Config{Ask: noAsk, Directory: &dir, Version: "test"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orgchart", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/orgchart = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	if !strings.Contains(rec.Body.String(), "org chart") {
		t.Errorf("body missing the org chart page")
	}
}

// TestOrgchartRouteDisabledWithoutDirectory confirms /orgchart is not served
// when no directory is configured, so it does not render a broken page.
func TestOrgchartRouteDisabledWithoutDirectory(t *testing.T) {
	t.Parallel()
	h, err := Handler(Config{Ask: noAsk})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orgchart", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("/orgchart without directory = %d, want 404", rec.Code)
	}
}
