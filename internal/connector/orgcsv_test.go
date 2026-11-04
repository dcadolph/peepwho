package connector

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// TestOrgCSVParse covers header mapping, topic splitting, and error paths.
func TestOrgCSVParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Name     string
		In       string
		Sep      string
		WantRecs []Record
		Want     error
	}{{ // Test 0: Basic row with all columns and aliases.
		Name: "basic",
		In: "Name,Email,Job Title,Team,Org,Topics\n" +
			"Jane Roe,jane@x.com,Staff Engineer,Billing,Payments,retries;idempotency\n",
		WantRecs: []Record{{
			Source: "org-csv", Weight: 1, Name: "Jane Roe", Email: "jane@x.com",
			Title: "Staff Engineer", Team: "Billing", Org: "Payments",
			Topics: []string{"retries", "idempotency"},
		}},
	}, { // Test 1: Header aliases and custom separator.
		Name: "aliases",
		In:   "person,mail,role,department,skills\nA B,a@x.com,SRE,Infra,kafka|kubernetes\n",
		Sep:  "|",
		WantRecs: []Record{{
			Source: "org-csv", Weight: 1, Name: "A B", Email: "a@x.com",
			Title: "SRE", Team: "Infra", Topics: []string{"kafka", "kubernetes"},
		}},
	}, { // Test 2: Rows missing both name and email are skipped.
		Name:     "skip empty",
		In:       "name,email\n,,\nKeep,keep@x.com\n",
		WantRecs: []Record{{Source: "org-csv", Weight: 1, Name: "Keep", Email: "keep@x.com"}},
	}, { // Test 3: No header row.
		Name: "no header",
		In:   "",
		Want: ErrNoHeader,
	}, { // Test 4: Header lacks name and email.
		Name: "missing required",
		In:   "title,team\nSRE,Infra\n",
		Want: ErrNoColumns,
	}}

	for _, test := range tests {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			o := &OrgCSV{TopicSep: test.Sep}
			got, err := o.parse(context.Background(), strings.NewReader(test.In))
			if !errors.Is(err, test.Want) {
				t.Fatalf("err = %v, want %v", err, test.Want)
			}
			if test.Want != nil {
				return
			}
			if diff := cmp.Diff(test.WantRecs, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("records mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestOrgCSVStripsBOM verifies a leading UTF-8 BOM does not hide the first
// column, which Excel and Windows exports commonly prepend.
func TestOrgCSVStripsBOM(t *testing.T) {
	t.Parallel()
	in := "\ufeffName,Email\nJane Roe,jane@x.com\n"
	got, err := (&OrgCSV{}).parse(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Jane Roe" || got[0].Email != "jane@x.com" {
		t.Errorf("records = %+v, want the Name column resolved past the BOM", got)
	}
}

// TestOrgCSVLazyQuotes verifies a stray quote in a field no longer aborts the
// parse, so later rows still load.
func TestOrgCSVLazyQuotes(t *testing.T) {
	t.Parallel()
	in := "Name,Email\nJ\"D Roe,jd@x.com\nBob,bob@x.com\n"
	got, err := (&OrgCSV{}).parse(context.Background(), strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("rows = %d (%+v), want both rows despite the stray quote", len(got), got)
	}
}
