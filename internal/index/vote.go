package index

import (
	"math"
	"strings"

	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/model"
)

// Feedback application defaults. A few votes adjust ranking; they never bury
// the underlying evidence. Both are tunable via SetFeedbackStrength.
const (
	// defaultFeedbackNet clamps the net votes applied to one result per query.
	defaultFeedbackNet = 3
	// defaultFeedbackStep is the per-vote score multiplier.
	defaultFeedbackStep = 1.25
)

// fbRule is one preprocessed vote: the stemmed query terms it applies to, the
// target entity, and the vote direction.
type fbRule struct {
	// terms are the stemmed tokens of the voted query.
	terms []string
	// id is the voted person or channel.
	id model.ID
	// vote is feedback.Helpful or feedback.NotHelpful.
	vote int
	// channel marks a channel vote.
	channel bool
}

// SetFeedbackStrength tunes how hard votes move ranking: step is the per-vote
// score multiplier and maxNet clamps the net votes applied to one result. A
// step at or below one, or a maxNet at or below zero, turns feedback off.
func (ix *Index) SetFeedbackStrength(step float64, maxNet int) {
	ix.fbStep = step
	ix.fbMax = maxNet
}

// SetFeedback gives the index user votes to apply during ranking. Later calls
// replace earlier ones. Person votes resolve through identity aliases, so a
// vote on any of a person's identifiers lands on the canonical entry.
func (ix *Index) SetFeedback(entries []feedback.Entry) {
	rules := make([]fbRule, 0, len(entries))
	for _, e := range entries {
		if !e.Valid() {
			continue
		}
		raw := tokenize(e.Query)
		if len(raw) == 0 {
			continue
		}
		terms := make([]string, len(raw))
		for i, t := range raw {
			terms[i] = stem(t)
		}
		r := fbRule{terms: terms, vote: e.Vote}
		if e.Person != "" {
			r.id = ix.identityResolver().Canonical(model.ID(strings.ToLower(e.Person)))
		} else {
			r.id = model.ID(slug(e.Channel))
			r.channel = true
		}
		rules = append(rules, r)
	}
	ix.fbRules.Store(&rules)
}

// feedbackNets sums the votes that apply to the current query, per entity,
// clamped to maxFeedbackNet in either direction. A vote applies when every
// term of its recorded query appears in the current one. The caller passes the
// terms the person typed, never the synonyms or ties the query grew by.
func (ix *Index) feedbackNets(queryTerms []string, channel bool) map[model.ID]int {
	step, maxNet := ix.feedbackStrength()
	if step <= 1 || maxNet <= 0 {
		return nil
	}
	loaded := ix.fbRules.Load()
	if loaded == nil || len(*loaded) == 0 {
		return nil
	}
	qset := make(map[string]bool, len(queryTerms))
	for _, t := range queryTerms {
		qset[stem(t)] = true
	}
	nets := make(map[model.ID]int)
	for _, r := range *loaded {
		if r.channel != channel {
			continue
		}
		applies := true
		for _, t := range r.terms {
			if !qset[t] {
				applies = false
				break
			}
		}
		if applies {
			nets[r.id] += r.vote
		}
	}
	for id, n := range nets {
		nets[id] = min(max(n, -maxNet), maxNet)
	}
	return nets
}

// feedbackStrength returns the configured step and clamp, defaulting when
// unset.
func (ix *Index) feedbackStrength() (float64, int) {
	step, maxNet := ix.fbStep, ix.fbMax
	if step == 0 {
		step = defaultFeedbackStep
	}
	if maxNet == 0 {
		maxNet = defaultFeedbackNet
	}
	return step, maxNet
}

// feedbackFactor converts a net vote count into a score multiplier.
func (ix *Index) feedbackFactor(net int) float64 {
	step, _ := ix.feedbackStrength()
	return math.Pow(step, float64(net))
}

// feedbackReason describes an applied net vote for the reasons list, or
// returns the empty string when no votes applied.
func feedbackReason(net int) string {
	switch {
	case net > 0:
		return "boosted by feedback"
	case net < 0:
		return "lowered by feedback"
	default:
		return ""
	}
}
