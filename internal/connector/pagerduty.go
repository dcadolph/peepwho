package connector

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/pagerduty"
	"github.com/dcadolph/peepwho/internal/util"
)

// PagerDutyOptions configures the PagerDuty connector.
type PagerDutyOptions struct {
	// Episodes records resolved incidents, so recall can point back at the
	// incident that was settled and who settled it.
	Episodes bool
	// IncidentDays bounds how far back incidents are read; zero uses a year.
	IncidentDays int
	// MaxIncidents caps incidents read; zero means all in the window.
	MaxIncidents int
	// Log receives progress lines; nil discards them.
	Log io.Writer
}

// withDefaults fills the log writer when unset.
func (o PagerDutyOptions) withDefaults() PagerDutyOptions {
	if o.Log == nil {
		o.Log = io.Discard
	}
	return o
}

// PagerDuty is a Source that ingests services and on-call assignments, giving
// each on-call person the topics of the services they answer for.
type PagerDuty struct {
	// client calls the PagerDuty API.
	client *pagerduty.Client
	// opts holds the resolved options.
	opts PagerDutyOptions
	// episodes holds the resolved incidents seen by the last Fetch.
	episodes []episode.Episode
}

// pagerdutyProgressEvery reports progress each time this many more incidents
// arrive.
const pagerdutyProgressEvery = 100

// NewPagerDuty returns a PagerDuty connector authenticating with token.
func NewPagerDuty(token string, opts PagerDutyOptions) *PagerDuty {
	o := opts.withDefaults()
	client := pagerduty.New(token,
		pagerduty.WithProgress(util.ProgressWriter(o.Log, "pagerduty: fetched", pagerdutyProgressEvery)))
	return &PagerDuty{client: client, opts: o}
}

// NewPagerDutyWithClient returns a PagerDuty connector using a preconfigured
// client. Tests use it to inject a client pointed at a mock server.
func NewPagerDutyWithClient(client *pagerduty.Client, opts PagerDutyOptions) *PagerDuty {
	if client == nil {
		panic("connector: NewPagerDutyWithClient requires a non-nil client")
	}
	return &PagerDuty{client: client, opts: opts.withDefaults()}
}

// Ping verifies the token with a cheap read-only call, so a wizard can confirm
// credentials before committing to a full index.
func (p *PagerDuty) Ping(ctx context.Context) error {
	return p.client.Ping(ctx)
}

// Fetch reads services and on-call assignments, returning one record per person
// weighted by the topics of the services they are on call for.
func (p *PagerDuty) Fetch(ctx context.Context) ([]Record, error) {
	p.episodes = nil
	if p.opts.Episodes {
		if err := p.collectIncidents(ctx); err != nil {
			// An incident history that cannot be read costs recall, not the
			// on-call graph the run is really for.
			fmt.Fprintf(p.opts.Log, "pagerduty: %v\n", err)
		}
	}
	services, err := p.client.Services(ctx)
	if err != nil {
		return nil, fmt.Errorf("pagerduty services: %w", err)
	}
	policyTopics := make(map[string][]string)
	for _, s := range services {
		tokens := append(phraseTokens(s.Name), titleTokens(s.Description)...)
		policyTopics[s.EscalationPolicy.ID] = append(policyTopics[s.EscalationPolicy.ID], tokens...)
	}

	oncalls, err := p.client.OnCalls(ctx)
	if err != nil {
		return nil, fmt.Errorf("pagerduty oncalls: %w", err)
	}
	fmt.Fprintf(p.opts.Log, "pagerduty: %d services, %d on-call assignments\n", len(services), len(oncalls))

	counts := make(map[string]map[string]int)
	users := make(map[string]pagerduty.User)
	bump := func(u pagerduty.User, tokens []string) {
		key := pagerdutyUserKey(u)
		if key == "" {
			return
		}
		m := counts[key]
		if m == nil {
			m = make(map[string]int)
			counts[key] = m
		}
		for _, t := range tokens {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				m[t]++
			}
		}
		users[key] = u
	}

	// One credit per person per policy. The on-call listing carries one entry
	// per escalation level and schedule, so a person on three tiers of one
	// policy arrived three times, and their weight measured escalation depth
	// rather than what they answer for.
	credited := make(map[string]bool)
	for _, oc := range oncalls {
		key := pagerdutyUserKey(oc.User)
		if key == "" {
			continue
		}
		pair := oc.EscalationPolicy.ID + "\x00" + key
		if credited[pair] {
			continue
		}
		credited[pair] = true
		bump(oc.User, policyTopics[oc.EscalationPolicy.ID])
	}

	records := make([]Record, 0, len(counts))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		m := counts[key]
		// PagerDuty has no label field: every topic here is mined from a service
		// name or description, so all of them are weak by construction.
		rec := pagerdutyPersonRecord(users[key], nil)
		rec.WeakTopics = expandTopics(m)
		records = append(records, rec)
	}
	return records, nil
}

// pagerdutyUserKey returns a stable key for a user, preferring email.
func pagerdutyUserKey(u pagerduty.User) string {
	return util.PersonKey("pagerduty", u.Email, u.ID)
}

// pagerdutyPersonRecord builds a person record. The user id always keys the
// record and any email travels with it, so the indexer joins the two and work
// recorded under a PagerDuty id is findable by the person who did it.
func pagerdutyPersonRecord(u pagerduty.User, topics []string) Record {
	rec := Record{Kind: KindPerson, Source: "pagerduty", Weight: 1, Topics: topics, Name: u.Name}
	if u.ID != "" {
		rec.PersonID = "pagerduty:" + u.ID
	}
	if u.Email != "" {
		rec.Email = util.NormalizeEmail(u.Email)
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
