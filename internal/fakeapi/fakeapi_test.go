package fakeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
)

// TestConfluenceSearchCapsBodyWindow verifies the fake serves a search that
// expands a page body in windows of at most fifty and names the window it
// used, while a search with no body keeps the window it asked for. The cap is
// what lets a test catch a client that advances by the window it asked for.
func TestConfluenceSearchCapsBodyWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Expand    string
		Limit     int
		WantSize  int
		WantLimit int
	}{{ // Test 0: A body search asking past the cap is cut to it.
		Expand: "space,body.storage", Limit: 100, WantSize: 50, WantLimit: 50,
	}, { // Test 1: A body search inside the cap is served as asked.
		Expand: "body.storage", Limit: 20, WantSize: 20, WantLimit: 20,
	}, { // Test 2: A search with no body keeps the wider window.
		Expand: "space,version", Limit: 100, WantSize: 100, WantLimit: 100,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			pages := make([]ConfluencePage, 120)
			for i := range pages {
				pages[i] = ConfluencePage{Title: fmt.Sprintf("page %03d", i), SpaceKey: "ENG"}
			}
			srv := (&Confluence{ServerMode: true, Pages: pages}).Server()
			t.Cleanup(srv.Close)

			params := url.Values{"expand": {test.Expand}, "limit": {strconv.Itoa(test.Limit)}}
			target := srv.URL + "/rest/api/content/search?" + params.Encode()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			defer resp.Body.Close()
			var got struct {
				Results []json.RawMessage `json:"results"`
				Size    int               `json:"size"`
				Limit   int               `json:"limit"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Results) != test.WantSize || got.Size != test.WantSize {
				t.Errorf("served %d rows and reported size %d, want %d",
					len(got.Results), got.Size, test.WantSize)
			}
			if got.Limit != test.WantLimit {
				t.Errorf("reported limit = %d, want %d", got.Limit, test.WantLimit)
			}
		})
	}
}
