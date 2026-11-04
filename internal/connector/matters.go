package connector

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Matters is a Source that reads a time-entry or matter export from a practice
// management or billing system. Every such system exports CSV, so this is the
// connector that works at any professional-services firm without an API
// partnership: who billed time to which matter, in which practice area, doing
// what. Billing narratives are the richest expertise signal a firm has,
// because people describe their own work in detail, per matter, timestamped.
//
// The header row is matched case-insensitively against known column names, so
// column order and exact spelling do not matter. An identity column and at
// least one of matter, practice area, or narrative are required.
type Matters struct {
	// Path is the CSV file path.
	Path string
	// RecentWindow bounds how far back an entry still counts as recent work;
	// default 180 days, matching the index's recency half-life.
	RecentWindow time.Duration
	// Log receives warnings about skipped rows; nil discards them.
	Log io.Writer
	// now returns the current time; tests fix it.
	now func() time.Time
}

// NewMatters returns a Matters connector reading the file at path.
func NewMatters(path string) *Matters {
	return &Matters{Path: path, RecentWindow: 180 * 24 * time.Hour, now: time.Now}
}

// Fetch reads the export and returns one record per timekeeper.
func (m *Matters) Fetch(ctx context.Context) ([]Record, error) {
	f, err := os.Open(m.Path)
	if err != nil {
		return nil, fmt.Errorf("matters: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	return m.parse(ctx, f)
}

// matterColumns holds the resolved index of each known column, or -1.
type matterColumns struct {
	// email indexes the timekeeper's email column.
	email int
	// name indexes the timekeeper's display-name column.
	name int
	// matter indexes the matter name or number column.
	matter int
	// practice indexes the practice area or group column.
	practice int
	// narrative indexes the work-description column.
	narrative int
	// date indexes the entry-date column.
	date int
}

// matterPerson accumulates one timekeeper's evidence across entries.
type matterPerson struct {
	// name is the display name, first non-empty wins.
	name string
	// email keys the person across sources.
	email string
	// curated are the subjects the system of record stated: practice areas
	// and matter names.
	curated []string
	// weak are the subjects mined from narrative prose.
	weak []string
	// recent are curated subjects from entries inside the recent window.
	recent []string
	// entries counts rows, so the record's weight reflects sustained work.
	entries int
}

// parse reads CSV rows from r and folds them into per-person records.
func (m *Matters) parse(ctx context.Context, r io.Reader) ([]Record, error) {
	logw := m.Log
	if logw == nil {
		logw = io.Discard
	}
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	window := m.RecentWindow
	if window <= 0 {
		window = 180 * 24 * time.Hour
	}
	cutoff := now().Add(-window)

	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("matters: read header: %w", err)
	}
	cols := matterHeader(header)
	if cols.email < 0 && cols.name < 0 {
		return nil, fmt.Errorf("matters: no timekeeper column: want one of email, timekeeper, attorney, or name")
	}
	if cols.matter < 0 && cols.practice < 0 && cols.narrative < 0 {
		return nil, fmt.Errorf("matters: nothing to learn from: want a matter, practice area, or narrative column")
	}

	people := map[string]*matterPerson{}
	var order []string
	get := func(row []string, i int) string {
		if i < 0 || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	line := 1
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			// Only a malformed row is skippable: the reader moves past it and
			// the next Read makes progress. Any other failure, such as an I/O
			// error, repeats forever, and skipping it looped printing the same
			// line number until the operator killed the run.
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				fmt.Fprintf(logw, "matters: line %d skipped: %v\n", line, err)
				continue
			}
			return nil, fmt.Errorf("matters: read row: %w", err)
		}
		email := strings.ToLower(get(row, cols.email))
		name := get(row, cols.name)
		key := email
		if key == "" {
			key = strings.ToLower(name)
		}
		if key == "" {
			fmt.Fprintf(logw, "matters: line %d skipped: no timekeeper\n", line)
			continue
		}
		p := people[key]
		if p == nil {
			p = &matterPerson{email: email}
			people[key] = p
			order = append(order, key)
		}
		if p.name == "" {
			p.name = name
		}
		p.entries++

		// The system of record STATED the practice area and the matter; the
		// narrative is prose the person typed. The stated-versus-mined split
		// is the same discipline every other source follows, and it is what
		// keeps a word used once in a description from becoming a subject.
		var curated []string
		curated = append(curated, phraseTokens(get(row, cols.practice))...)
		curated = append(curated, phraseTokens(get(row, cols.matter))...)
		p.curated = append(p.curated, curated...)
		p.weak = append(p.weak, titleTokens(get(row, cols.narrative))...)
		if when, ok := matterDate(get(row, cols.date)); ok && when.After(cutoff) {
			p.recent = append(p.recent, curated...)
		}
	}

	records := make([]Record, 0, len(order))
	for _, key := range order {
		p := people[key]
		records = append(records, Record{
			Kind:         KindPerson,
			Name:         p.name,
			Email:        p.email,
			Topics:       p.curated,
			WeakTopics:   p.weak,
			RecentTopics: p.recent,
			Source:       "matters",
		})
	}
	return records, nil
}

// matterHeader resolves the known columns from a header row.
func matterHeader(header []string) matterColumns {
	cols := matterColumns{email: -1, name: -1, matter: -1, practice: -1, narrative: -1, date: -1}
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "email", "timekeeper email", "attorney email", "user email":
			cols.email = i
		case "name", "timekeeper", "attorney", "user", "timekeeper name":
			cols.name = i
		case "matter", "matter name", "matter number", "case", "engagement", "project":
			cols.matter = i
		case "practice", "practice area", "practice group", "area", "department":
			cols.practice = i
		case "narrative", "description", "work description", "notes", "entry":
			cols.narrative = i
		case "date", "entry date", "work date", "worked":
			cols.date = i
		}
	}
	return cols
}

// matterDate parses the date formats billing exports actually use.
func matterDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02", "01/02/2006", "1/2/2006", "2006/01/02", "02-Jan-2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
