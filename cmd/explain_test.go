package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/dcadolph/whotoask/internal/httputil"
)

// TestExplainSourceError verifies an auth or not-found status becomes a message
// that names the source and the credential to fix, while other errors pass
// through untouched. A bare status code next to an internal API path tells a
// paying customer nothing about what to do.
func TestExplainSourceError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In       error
		WantAuth bool
		WantHas  []string
	}{{ // Test 0: 401 names the token.
		In:       fmt.Errorf("jira /rest/api/3/search: %w", &httputil.StatusError{Code: http.StatusUnauthorized}),
		WantAuth: true, WantHas: []string{"Jira", "WHOTOASK_JIRA_TOKEN", "401"},
	}, { // Test 1: 403 is also an auth fix.
		In:       fmt.Errorf("wrap: %w", &httputil.StatusError{Code: http.StatusForbidden}),
		WantAuth: true, WantHas: []string{"Jira"},
	}, { // Test 2: 404 points at the URL and scope.
		In:       fmt.Errorf("wrap: %w", &httputil.StatusError{Code: http.StatusNotFound}),
		WantAuth: true, WantHas: []string{"404", "not found"},
	}, { // Test 3: A 500 is not remapped; the caller sees the original.
		In:       fmt.Errorf("wrap: %w", &httputil.StatusError{Code: http.StatusInternalServerError}),
		WantAuth: false,
	}, { // Test 4: A non-status error passes through unchanged.
		In: errors.New("dial tcp: connection refused"), WantAuth: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			got := explainSourceError("Jira", "WHOTOASK_JIRA_TOKEN", test.In)
			if errors.Is(got, ErrAuth) != test.WantAuth {
				t.Errorf("ErrAuth = %v, want %v (got %v)", errors.Is(got, ErrAuth), test.WantAuth, got)
			}
			for _, want := range test.WantHas {
				if !strings.Contains(got.Error(), want) {
					t.Errorf("message %q missing %q", got, want)
				}
			}
			if !test.WantAuth && !errors.Is(got, test.In) {
				t.Errorf("non-auth error was rewritten: %v", got)
			}
		})
	}
}
