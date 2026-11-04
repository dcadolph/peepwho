package connector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/pagerduty"
	"github.com/dcadolph/peepwho/internal/util"
)

// Incident window bounds. The PagerDuty incidents API rejects a date range
// wider than six months, so the default sits inside it and anything larger is
// clamped rather than silently failing the whole read.
const (
	// defaultIncidentDays is how far back incidents are read by default.
	defaultIncidentDays = 180
	// maxIncidentDays is the widest range the API accepts.
	maxIncidentDays = 180
)

// Episodes returns the resolved incidents seen by the most recent Fetch. It is
// empty unless PagerDutyOptions.Episodes was set.
func (p *PagerDuty) Episodes() []episode.Episode { return p.episodes }

// collectIncidents records resolved incidents: what broke, who settled it, and
// a link to the incident. An incident nobody was assigned to is skipped, since
// recall answers with people.
func (p *PagerDuty) collectIncidents(ctx context.Context) error {
	days := p.opts.IncidentDays
	if days <= 0 {
		days = defaultIncidentDays
	}
	if days > maxIncidentDays {
		fmt.Fprintf(p.opts.Log,
			"pagerduty: incident window capped at %d days by the API\n", maxIncidentDays)
		days = maxIncidentDays
	}
	since := time.Now().AddDate(0, 0, -days)
	incidents, err := p.client.Incidents(ctx, since, p.opts.MaxIncidents)
	if err != nil {
		return fmt.Errorf("pagerduty incidents: %w", err)
	}
	// Offset paging cannot reach past the ceiling, so a run that hits it read
	// only the most recent incidents. Say so rather than let the count read as
	// complete, unless the caller's own cap is what stopped it.
	if (p.opts.MaxIncidents <= 0 || p.opts.MaxIncidents > pagerduty.IncidentCeiling) &&
		len(incidents) >= pagerduty.IncidentCeiling {
		fmt.Fprintf(p.opts.Log,
			"pagerduty: reached the %d incident ceiling; older incidents in the window were not read\n",
			pagerduty.IncidentCeiling)
	}
	for _, in := range incidents {
		ep, ok := incidentEpisode(in)
		if !ok {
			continue
		}
		// Fold the triage and resolution notes into the body so recall can match
		// how the incident was settled, not only its title and service. A note
		// fetch failure costs detail, not the episode, so it is logged.
		if notes, err := p.client.IncidentNotes(ctx, in.ID); err != nil {
			fmt.Fprintf(p.opts.Log, "pagerduty: notes for %s: %v\n", in.ID, err)
		} else if t := pagerDutyNotesText(notes); t != "" {
			ep.Body = strings.TrimSpace(ep.Body + " " + t)
		}
		p.episodes = append(p.episodes, ep)
	}
	return nil
}

// pagerDutyNotesText joins an incident's note contents into one searchable
// string.
func pagerDutyNotesText(notes []pagerduty.Note) string {
	parts := make([]string, 0, len(notes))
	for _, n := range notes {
		if s := strings.TrimSpace(n.Content); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// incidentEpisode builds an episode from a resolved incident. The episode
// carries no asker: PagerDuty records assignees, acknowledgers, and whoever
// last changed the status, and all three are people who answered. An incident
// is opened by an alert rather than by a person asking, and the API exposes no
// reporter, so there is nobody to name.
func incidentEpisode(in pagerduty.Incident) (episode.Episode, bool) {
	if !in.Resolved() {
		return episode.Episode{}, false
	}
	people := in.People()
	participants := make([]model.ID, 0, len(people))
	seen := make(map[model.ID]bool, len(people))
	for _, u := range people {
		id := model.ID(pagerDutyPersonID(u))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		participants = append(participants, id)
	}
	if len(participants) == 0 {
		return episode.Episode{}, false
	}
	when := in.ResolvedAt
	if when.IsZero() {
		when = in.CreatedAt
	}
	id := in.ID
	if id == "" {
		id = strconv.Itoa(in.Number)
	}
	return episode.Episode{
		ID:           "pagerduty:" + id,
		Source:       "pagerduty",
		Kind:         episode.KindIncident,
		Place:        in.Service.Summary,
		Title:        in.Title,
		Participants: participants,
		Occurred:     when,
		Permalink:    in.HTMLURL,
		Body:         strings.TrimSpace(in.Title + " " + in.Description + " " + in.Service.Summary),
	}, true
}

// pagerDutyPersonID resolves a PagerDuty user to the identifier the index keys
// them by: their email when known, otherwise their PagerDuty id.
func pagerDutyPersonID(u pagerduty.User) string {
	if u.Email != "" {
		return util.NormalizeEmail(u.Email)
	}
	if u.ID == "" {
		return ""
	}
	return "pagerduty:" + u.ID
}
