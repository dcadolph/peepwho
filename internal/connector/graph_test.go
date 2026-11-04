package connector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dcadolph/peepwho/internal/graph"
)

// oneBody is an HTTP doer that returns the same body for every request.
type oneBody struct{ body string }

func (o oneBody) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(o.body)),
		Header:     make(http.Header),
	}, nil
}

// TestGraphFetchMapsFields confirms the connector maps the manager, department,
// and source onto the record.
func TestGraphFetchMapsFields(t *testing.T) {
	t.Parallel()
	body := `{"value":[{"id":"1","displayName":"Alice","mail":"alice@x.com","jobTitle":"Eng","department":"Pay","manager":{"mail":"boss@x.com"}}]}`
	c := graph.New("tok", graph.WithHTTPClient(oneBody{body}))
	recs, err := NewGraph(c).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1", len(recs))
	}
	r := recs[0]
	if r.Manager != "boss@x.com" {
		t.Errorf("manager = %q, want boss@x.com", r.Manager)
	}
	if r.Team != "Pay" || r.Title != "Eng" || r.Email != "alice@x.com" {
		t.Errorf("mapped fields wrong: %+v", r)
	}
	if r.Source != "graph" {
		t.Errorf("source = %q, want graph", r.Source)
	}
}
