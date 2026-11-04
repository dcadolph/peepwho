package connector

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// OrgCSV is a Source that reads an organization chart from a CSV file. The
// header row is matched case-insensitively against known column names, so
// column order and exact spelling do not matter.
type OrgCSV struct {
	// Path is the CSV file path.
	Path string
	// TopicSep splits the topics column into individual topics; default ";".
	TopicSep string
	// Log receives warnings about skipped rows; nil discards them.
	Log io.Writer
}

// NewOrgCSV returns an OrgCSV reading the file at path with default settings.
func NewOrgCSV(path string) *OrgCSV {
	return &OrgCSV{Path: path, TopicSep: ";"}
}

// Fetch reads the CSV and returns one record per non-empty data row.
func (o *OrgCSV) Fetch(ctx context.Context) ([]Record, error) {
	f, err := os.Open(o.Path)
	if err != nil {
		return nil, fmt.Errorf("org csv: open: %w", err)
	}
	defer func() { _ = f.Close() }()
	return o.parse(ctx, f)
}

// columns holds the resolved index of each known column, or -1 if absent.
type columns struct {
	// name indexes the display-name column.
	name int
	// email indexes the email column.
	email int
	// title indexes the job-title column.
	title int
	// team indexes the team column.
	team int
	// org indexes the organization column.
	org int
	// manager indexes the manager column.
	manager int
	// topics indexes the topics or skills column.
	topics int
}

// parse reads CSV rows from r and converts them into records.
func (o *OrgCSV) parse(ctx context.Context, r io.Reader) ([]Record, error) {
	logw := o.Log
	if logw == nil {
		logw = io.Discard
	}

	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	cr.LazyQuotes = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, ErrNoHeader
	}
	if err != nil {
		return nil, fmt.Errorf("org csv: read header: %w", err)
	}

	cols := mapColumns(header)
	if cols.name < 0 && cols.email < 0 {
		return nil, fmt.Errorf("%w: need a name or email column", ErrNoColumns)
	}

	sep := o.TopicSep
	if sep == "" {
		sep = ";"
	}

	var records []Record
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				fmt.Fprintf(logw, "org csv: skipping malformed row: %v\n", err)
				continue
			}
			return nil, fmt.Errorf("org csv: read row: %w", err)
		}

		rec := Record{
			Source:  "org-csv",
			Weight:  1,
			Name:    at(row, cols.name),
			Email:   at(row, cols.email),
			Title:   at(row, cols.title),
			Team:    at(row, cols.team),
			Org:     at(row, cols.org),
			Manager: at(row, cols.manager),
		}
		if t := at(row, cols.topics); t != "" {
			rec.Topics = splitTopics(t, sep)
		}
		if rec.Name == "" && rec.Email == "" {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

// mapColumns resolves known fields to their column index in the header.
func mapColumns(header []string) columns {
	c := columns{name: -1, email: -1, title: -1, team: -1, org: -1, manager: -1, topics: -1}
	for i, h := range header {
		switch normalizeHeader(h) {
		case "name", "fullname", "employee", "person":
			c.name = i
		case "email", "mail", "emailaddress":
			c.email = i
		case "title", "jobtitle", "role":
			c.title = i
		case "team", "department", "dept", "group":
			c.team = i
		case "org", "organization", "division", "businessunit":
			c.org = i
		case "manager", "manageremail", "reportsto":
			c.manager = i
		case "topics", "skills", "tags", "expertise":
			c.topics = i
		}
	}
	return c
}

// normalizeHeader lowercases a header and strips a leading UTF-8 BOM, spaces,
// and separators. TrimSpace does not remove the BOM, so a CSV exported by Excel
// or Windows would otherwise lose its first column.
func normalizeHeader(h string) string {
	h = strings.TrimPrefix(h, "\ufeff")
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.ReplaceAll(h, " ", "")
	h = strings.ReplaceAll(h, "_", "")
	h = strings.ReplaceAll(h, "-", "")
	return h
}

// at returns the trimmed cell at index i, or "" if i is out of range.
func at(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// splitTopics splits s on sep and returns the non-empty, trimmed parts.
func splitTopics(s, sep string) []string {
	var out []string
	for p := range strings.SplitSeq(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
