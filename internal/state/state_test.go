package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// fixedTime pins a timestamp so round trips are deterministic.
var fixedTime = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

// TestRoundTrip verifies a watermark survives Save and Load unchanged.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "index.state.json")
	s := New()
	wm := Watermark{Source: "jira", Scope: "project:SEC,SUP", Cursor: fixedTime, Complete: true, RanAt: fixedTime}
	s.Set(wm)
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotWM, ok := got.Get("jira", "project:SEC,SUP")
	if !ok {
		t.Fatal("watermark missing after round trip")
	}
	if diff := cmp.Diff(wm, gotWM); diff != "" {
		t.Errorf("watermark mismatch (-want +got):\n%s", diff)
	}
}

// TestLoadMissingIsEmpty verifies a missing file yields an empty state rather
// than an error, so a first run is a full index.
func TestLoadMissingIsEmpty(t *testing.T) {
	t.Parallel()
	s, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if s == nil || len(s.Watermarks) != 0 || s.Version != Version {
		t.Errorf("Load missing = %+v, want empty state at version %d", s, Version)
	}
}

// TestGetSetDelete verifies the in-memory watermark map operations.
func TestGetSetDelete(t *testing.T) {
	t.Parallel()
	s := New()
	if _, ok := s.Get("jira", "x"); ok {
		t.Error("empty state returned a watermark")
	}
	s.Set(Watermark{Source: "jira", Scope: "x", Cursor: fixedTime})
	if _, ok := s.Get("jira", "x"); !ok {
		t.Error("Set watermark not found by Get")
	}
	s.Delete("jira", "x")
	if _, ok := s.Get("jira", "x"); ok {
		t.Error("Delete did not remove the watermark")
	}
}

// TestKey verifies distinct source and scope pairs never share a key.
func TestKey(t *testing.T) {
	t.Parallel()
	if Key("jira", "a") == Key("jira", "b") {
		t.Error("different scopes produced the same key")
	}
	if Key("jira", "a") == Key("confluence", "a") {
		t.Error("different sources produced the same key")
	}
	if Key("a", "b") == Key("a\x00b", "") {
		t.Error("separator collision between source and scope")
	}
}

// TestWatermarkKeepsItsZone checks a cursor survives the state file with the
// offset it was written in, not flattened to UTC.
//
// The incremental boundary for Jira and Confluence is a bare wall clock, since
// neither query language accepts a timezone, and it is only correct because the
// cursor still reads as the site's own clock. Storing the instant alone would be
// enough to compare times and not enough to ask for them: measured against a
// live server, the same moment carrying +05:30 instead of Z moved the boundary
// five and a half hours into the future and returned nothing.
func TestWatermarkKeepsItsZone(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("CDT", -5*3600)
	cursor := time.Date(2026, 8, 25, 14, 18, 2, 0, loc)
	path := filepath.Join(t.TempDir(), "index.state.json")

	st := New()
	st.Set(Watermark{Source: "jira", Scope: "project:K", Cursor: cursor, Complete: true})
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := back.Get("jira", "project:K")
	if !ok {
		t.Fatal("the watermark did not survive the file at all")
	}
	if !got.Cursor.Equal(cursor) {
		t.Errorf("cursor = %v, want the same instant %v", got.Cursor, cursor)
	}
	const wall = "2006/01/02 15:04"
	if got.Cursor.Format(wall) != cursor.Format(wall) {
		t.Errorf("cursor reads as %q, want the site's own clock %q: the zone was lost, "+
			"so the next incremental read asks for the wrong hour",
			got.Cursor.Format(wall), cursor.Format(wall))
	}
}
