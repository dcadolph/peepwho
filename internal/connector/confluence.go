package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/confluence"
	"github.com/dcadolph/whotoask/internal/util"
)

// ConfluenceOptions configures the Confluence connector.
type ConfluenceOptions struct {
	// Spaces scopes the search to these space keys.
	Spaces []string
	// CQL overrides the query entirely when set.
	CQL string
	// MaxPages caps pages read; zero uses a default.
	MaxPages int
	// Server selects a self-hosted Server or Data Center deployment, which
	// serves the REST API at the site root and authenticates with a bearer
	// token or not at all, rather than Cloud's /wiki path and email-plus-token.
	Server bool
	// Since, when set, limits the read to pages last modified at or after it, for
	// an incremental re-index. It is ignored when CQL overrides the query.
	Since time.Time
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// withDefaults fills the log writer and page cap when unset.
func (o ConfluenceOptions) withDefaults() ConfluenceOptions {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 2000
	}
	return o
}

// Confluence is a Source that ingests pages and weights their creator and last
// editor by the labels, title words, and space of the pages they wrote.
type Confluence struct {
	// client calls the Confluence API.
	client *confluence.Client
	// opts holds the resolved options.
	opts ConfluenceOptions
}

// confluenceProgressEvery reports progress each time this many more pages
// arrive.
const confluenceProgressEvery = 100

// NewConfluence returns a Confluence connector for the site, authenticating with
// an email and API token.
func NewConfluence(siteURL, email, token string, opts ConfluenceOptions) *Confluence {
	o := opts.withDefaults()
	progress := confluence.WithProgress(
		util.ProgressWriter(o.Log, "confluence: fetched", confluenceProgressEvery))
	var client *confluence.Client
	if o.Server {
		client = confluence.NewServer(siteURL, token, progress)
	} else {
		client = confluence.New(siteURL, email, token, progress)
	}
	return &Confluence{client: client, opts: o}
}

// NewConfluenceWithClient returns a Confluence connector using a preconfigured
// client. Tests use it to inject a client pointed at a mock server.
func NewConfluenceWithClient(client *confluence.Client, opts ConfluenceOptions) *Confluence {
	if client == nil {
		panic("connector: NewConfluenceWithClient requires a non-nil client")
	}
	return &Confluence{client: client, opts: opts.withDefaults()}
}

// Ping verifies the credentials with a cheap current-user call, so a wizard can
// confirm the site, email, and token before committing to a full index.
func (c *Confluence) Ping(ctx context.Context) error {
	return c.client.Ping(ctx)
}

// Fetch searches pages and returns one record per person, weighted by topic.
func (c *Confluence) Fetch(ctx context.Context) ([]Record, error) {
	q := confluence.Query{Spaces: c.opts.Spaces, CQL: c.opts.CQL, Max: c.opts.MaxPages, Since: c.opts.Since}
	pages, err := c.client.Pages(ctx, q)
	switch {
	case errors.Is(err, confluence.ErrTruncated):
		// The read is partial, not broken: index what came back and say what
		// did not, rather than logging a capped wiki as if it were whole.
		fmt.Fprintf(c.opts.Log,
			"confluence: %v. Everything past the --max-pages cap of %d is invisible to this "+
				"index; raise --max-pages to read it\n", err, c.opts.MaxPages)
	case err != nil:
		return nil, fmt.Errorf("confluence search: %w", err)
	}
	fmt.Fprintf(c.opts.Log, "confluence: %d pages for %q\n", len(pages), confluenceQueryLabel(q))

	counts := make(map[string]map[string]int)
	ties := newTogether()
	// Tokens a page stated as a label. Title, space, and body words stay weak.
	curated := make(map[string]bool)
	users := make(map[string]confluence.User)
	latest := make(map[string]time.Time)
	bump := func(u *confluence.User, tokens []string, t time.Time) {
		if u == nil {
			return
		}
		key := confluenceUserKey(*u)
		if key == "" {
			return
		}
		m := counts[key]
		if m == nil {
			m = make(map[string]int)
			counts[key] = m
		}
		for _, tok := range tokens {
			if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
				m[tok]++
			}
		}
		if t.After(latest[key]) {
			latest[key] = t
		}
		users[key] = *u
	}

	for _, page := range pages {
		tokens := pageTopics(page)
		stated := make([]string, 0, 4)
		for _, tok := range page.LabelNames() {
			if tok = strings.ToLower(strings.TrimSpace(tok)); tok != "" {
				curated[tok] = true
				stated = append(stated, tok)
			}
		}
		creator, editor := page.History.CreatedBy, page.Version.By
		// What one page states is written about together, and whoever last
		// wrote it is who worked across those subjects. Only the labels count:
		// the words of a title or a body are prose, and pairing those would tie
		// a subject to every phrase that happened to sit near it.
		who := ""
		if editor != nil {
			who = confluenceUserKey(*editor)
		} else if creator != nil {
			who = confluenceUserKey(*creator)
		}
		ties.note(stated, who, page.Space.Key)
		// Credit the creator at creation time and the last editor at edit time,
		// so an old page edited yesterday does not make its author look recently
		// active. A person who did both is credited once, at their later action.
		editorIsCreator := creator != nil && editor != nil &&
			confluenceUserKey(*creator) == confluenceUserKey(*editor)
		if creator != nil {
			when := page.History.CreatedAt
			if editorIsCreator && page.Version.When.After(when) {
				when = page.Version.When
			}
			bump(creator, tokens, when)
		}
		if editor != nil && !editorIsCreator {
			bump(editor, tokens, page.Version.When)
		}
	}

	records := make([]Record, 0, len(counts))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		m := counts[key]
		rec := confluencePersonRecord(users[key], nil)
		rec.Topics, rec.WeakTopics = splitCurated(expandTopics(m), curated)
		rec.Time = latest[key]
		records = append(records, rec)
	}
	records = append(records, ties.records("confluence")...)
	return records, nil
}

// confluenceQueryLabel describes a query for a progress line: the raw CQL, the
// scoped spaces, or all spaces.
func confluenceQueryLabel(q confluence.Query) string {
	switch {
	case strings.TrimSpace(q.CQL) != "":
		return q.CQL
	case len(q.Spaces) > 0:
		return "spaces " + strings.Join(q.Spaces, ", ")
	default:
		return "all spaces"
	}
}

// maxPageBodyText caps how much of a page body is mined for topics, so one long
// page cannot dominate an author's profile.
const maxPageBodyText = 8000

// pageTopics derives topic tokens from a page's labels, title, space name, and
// body. The body is where a runbook's substance lives; without it "who knows
// idempotency keys" misses the author of a page titled "Checkout notes" whose
// body is entirely about idempotency. Body text is redacted to stemmed terms on
// save like any other text.
func pageTopics(p confluence.Page) []string {
	var out []string
	out = append(out, p.LabelNames()...)
	out = append(out, phraseTokens(p.Title)...)
	out = append(out, titleTokens(p.Space.Name)...)
	if body := p.BodyText(); body != "" {
		out = append(out, titleTokens(util.Truncate(body, maxPageBodyText))...)
	}
	return out
}

// confluenceUserKey returns a stable key for a user, preferring email.
func confluenceUserKey(u confluence.User) string {
	return util.PersonKey("confluence", u.Email, u.Identity())
}

// confluencePersonRecord builds a person record. An email lets the person join
// other sources; otherwise the account id keys the record.
func confluencePersonRecord(u confluence.User, topics []string) Record {
	rec := Record{Kind: KindPerson, Source: "confluence", Weight: 1, Topics: topics, Name: u.DisplayName}
	if u.Email != "" {
		rec.Email = util.NormalizeEmail(u.Email)
	} else if id := u.Identity(); id != "" {
		rec.PersonID = "confluence:" + id
	}
	if rec.Name == "" {
		if rec.Email != "" {
			rec.Name = rec.Email
		} else {
			rec.Name = rec.PersonID
		}
	}
	return rec
}
