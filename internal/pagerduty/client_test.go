package pagerduty

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestServicesAndOnCalls verifies both endpoints decode.
func TestServicesAndOnCalls(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/services", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"more":false,"services":[{"id":"S1","name":"Billing API",`+
			`"description":"Handles billing and payments","escalation_policy":{"id":"EP1"}}]}`)
	})
	mux.HandleFunc("/oncalls", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"more":false,"oncalls":[{`+
			`"user":{"id":"U1","name":"Jane Roe","email":"jane@x.com"},`+
			`"escalation_policy":{"id":"EP1"}}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token", WithBaseURL(srv.URL))

	services, err := c.Services(context.Background())
	if err != nil || len(services) != 1 || services[0].Name != "Billing API" ||
		services[0].EscalationPolicy.ID != "EP1" {
		t.Fatalf("services = %v, err %v", services, err)
	}
	oncalls, err := c.OnCalls(context.Background())
	if err != nil || len(oncalls) != 1 || oncalls[0].User.Email != "jane@x.com" ||
		oncalls[0].EscalationPolicy.ID != "EP1" {
		t.Fatalf("oncalls = %v, err %v", oncalls, err)
	}
}

// TestNewEmptyTokenPanics verifies the constructor guards an empty token.
func TestNewEmptyTokenPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("New(\"\") did not panic")
		}
	}()
	New("")
}

// TestIncidents verifies paging, the query the endpoint sends, the max cap,
// and that the people on an incident decode and deduplicate.
func TestIncidents(t *testing.T) {
	t.Parallel()
	var queries []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query())
		if r.URL.Query().Get("offset") == "0" {
			io.WriteString(w, `{"more":true,"incidents":[{
				"id":"I1","incident_number":7,"title":"DB down","status":"resolved",
				"html_url":"https://x.pagerduty.com/incidents/I1",
				"created_at":"2026-08-01T10:00:00Z","resolved_at":"2026-08-01T11:00:00Z",
				"service":{"id":"S1","summary":"Billing API"},
				"assignments":[{"assignee":{"id":"U1","name":"Jane Roe","email":"jane@x.com"}}],
				"acknowledgements":[
					{"acknowledger":{"id":"U1","name":"Jane Roe","email":"jane@x.com"}},
					{"acknowledger":{"id":"U2","name":"Sam Lee","email":"sam@x.com"}}]}]}`)
			return
		}
		io.WriteString(w, `{"more":false,"incidents":[{
			"id":"I2","incident_number":8,"title":"Queue stuck","status":"resolved",
			"created_at":"2026-07-20T09:00:00Z"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token", WithBaseURL(srv.URL))

	since := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Incidents(context.Background(), since, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("Incidents = %d incidents, err %v", len(got), err)
	}
	if got[0].ID != "I1" || got[0].Number != 7 || got[0].Service.Summary != "Billing API" {
		t.Errorf("first incident = %+v", got[0])
	}
	if !got[0].Resolved() || !got[1].Resolved() {
		t.Error("incidents did not report resolved")
	}
	people := got[0].People()
	if len(people) != 2 || people[0].ID != "U1" || people[1].ID != "U2" {
		t.Errorf("People() = %+v, want U1 and U2 once each", people)
	}
	if len(queries) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(queries))
	}
	q := queries[0]
	if q.Get("statuses[]") != "resolved" || q.Get("since") != "2026-07-01T00:00:00Z" ||
		q.Get("until") == "" || q.Get("sort_by") != "created_at:desc" {
		t.Errorf("first query = %v", q)
	}
	if queries[1].Get("offset") != "100" {
		t.Errorf("second query offset = %q, want 100", queries[1].Get("offset"))
	}
}

// TestIncidentsMax verifies the cap stops paging and trims the tail.
func TestIncidentsMax(t *testing.T) {
	t.Parallel()
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		io.WriteString(w, `{"more":true,"incidents":[{"id":"A","status":"resolved"},`+
			`{"id":"B","status":"resolved"},{"id":"C","status":"resolved"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New("token", WithBaseURL(srv.URL))

	got, err := c.Incidents(context.Background(), time.Time{}, 2)
	if err != nil || len(got) != 2 || got[0].ID != "A" || got[1].ID != "B" {
		t.Fatalf("Incidents = %+v, err %v, want A and B", got, err)
	}
	if calls != 1 {
		t.Errorf("server saw %d requests, want 1 because the cap was hit", calls)
	}
}

// TestIncidentsStatusError verifies a non-200 status surfaces as ErrStatus.
func TestIncidentsStatusError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	c := New("token", WithBaseURL(srv.URL))

	if _, err := c.Incidents(context.Background(), time.Time{}, 0); !errors.Is(err, ErrStatus) {
		t.Fatalf("Incidents error = %v, want ErrStatus", err)
	}
}
