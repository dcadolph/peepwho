package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// jsonRecord is the JSON shape the JSON source accepts. Its field names are the
// friendly lowercase form of Record, so an external system can emit records
// without knowing Go field names. It maps onto Record in toRecord.
type jsonRecord struct {
	// Kind is "person" (the default) or "channel".
	Kind string `json:"kind"`
	// ID is a stable per-source identifier; empty derives one from email.
	ID string `json:"id"`
	// Name is the person's display name, or the channel name.
	Name string `json:"name"`
	// Email is the person's work email.
	Email string `json:"email"`
	// Title is the person's job title, or the channel topic.
	Title string `json:"title"`
	// Team is the person's team name.
	Team string `json:"team"`
	// Org is the person's organization name.
	Org string `json:"org"`
	// Manager is the manager's email or identifier, if known.
	Manager string `json:"manager"`
	// Topics are explicit expertise tags.
	Topics []string `json:"topics"`
	// Members lists person references active in a channel record.
	Members []string `json:"members"`
	// Text is free-form text mined for topics; it is never written to disk.
	Text string `json:"text"`
	// Source names the origin; empty falls back to the source's default label.
	Source string `json:"source"`
	// Weight scales the record's affinity contribution; zero means one.
	Weight float64 `json:"weight"`
	// Time is when the activity happened; the zero value is a current fact.
	Time time.Time `json:"time"`
}

// JSON reads records as a JSON array from a reader, the generic import path for
// any system that can emit JSON. It lets a catalog, an HR export, or a one-off
// script feed the graph without a dedicated connector.
type JSON struct {
	// r is the source of the JSON array.
	r io.Reader
	// defaultSource labels records that do not carry their own source.
	defaultSource string
}

// NewJSON returns a JSON source reading from r. Records without a source field
// are labeled defaultSource.
func NewJSON(r io.Reader, defaultSource string) *JSON {
	if r == nil {
		panic("connector.NewJSON: reader required")
	}
	if defaultSource == "" {
		defaultSource = "json"
	}
	return &JSON{r: r, defaultSource: defaultSource}
}

// Fetch decodes the JSON array and returns the normalized records. A malformed
// array, an unknown record kind, or data after the array is an error, so a bad
// import fails loudly rather than silently dropping records. The trailing
// check matters because Decode stops at the first complete value: without it,
// a file holding two arrays imported only the first and reported success.
func (j *JSON) Fetch(_ context.Context) ([]Record, error) {
	dec := json.NewDecoder(j.r)
	var raw []jsonRecord
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("json source: decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("json source: trailing data after the record array")
	}
	recs := make([]Record, 0, len(raw))
	for i, jr := range raw {
		rec, err := j.toRecord(jr)
		if err != nil {
			return nil, fmt.Errorf("json source: record %d: %w", i, err)
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// toRecord maps one jsonRecord onto a Record, defaulting the source label.
func (j *JSON) toRecord(jr jsonRecord) (Record, error) {
	kind, err := parseRecordKind(jr.Kind)
	if err != nil {
		return Record{}, err
	}
	source := jr.Source
	if source == "" {
		source = j.defaultSource
	}
	return Record{
		Kind:     kind,
		PersonID: jr.ID,
		Name:     jr.Name,
		Email:    jr.Email,
		Title:    jr.Title,
		Team:     jr.Team,
		Org:      jr.Org,
		Manager:  jr.Manager,
		Topics:   jr.Topics,
		Members:  jr.Members,
		Text:     jr.Text,
		Source:   source,
		Weight:   jr.Weight,
		Time:     jr.Time,
	}, nil
}

// parseRecordKind reads the friendly kind label. An empty value is KindPerson,
// matching the Record zero value.
func parseRecordKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "person":
		return KindPerson, nil
	case "channel":
		return KindChannel, nil
	default:
		return 0, fmt.Errorf("unknown kind %q: want person or channel", s)
	}
}
