// Package eval measures how well whotoask answers, against the only ground truth
// a real organization hands over for free: the ownership it wrote down.
//
// The point is not the score. The point is that a score exists at all, and that
// the same index measured twice gives the same one, so a change to ranking or
// identity can be shown to help rather than argued to. Several plausible
// improvements to this codebase were measured and turned out to make things
// worse; without a number they would all have shipped.
package eval

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// Result is everything measurable about one index, in one place.
//
// It deliberately mixes the score with the reasons a score could be wrong.
// Agreement alone is not interpretable: a low number means whotoask is wrong, or
// the declared owner is stale, or the owner's work was never linked to their
// handle, and those call for three different fixes.
type Result struct {
	// Agreement is how often the declared owner of an area is also the person
	// whotoask says leads it, zero to one. Read it with Unlinked in hand: an owner
	// whose commits were never tied to their handle cannot agree with anything.
	Agreement float64 `json:"agreement"`
	// Declared is how many areas have an owner of record at all, which is the
	// number Agreement is out of.
	Declared int `json:"declared"`
	// Held is how many of those the declared owner still leads.
	Held int `json:"held"`
	// Silent counts disagreements where the declared owner has no recorded work
	// anywhere. Usually an identity that was never joined, not a departure.
	Silent int `json:"silent"`
	// Unworked counts disagreements where the owner works elsewhere but never in
	// the area they own. Paper ownership, and whotoask is probably right.
	Unworked int `json:"unworked"`
	// Trailing counts disagreements where the owner does work in the area, just
	// less than whoever leads it. The genuinely arguable bucket.
	Trailing int `json:"trailing"`
	// Answerable is how many declared areas the score can fairly be asked about:
	// the ones where the declared owner has recorded work in that area at all.
	// An owner who never appears in what was indexed cannot be ranked first no
	// matter how good the ranking is, so counting them scores coverage.
	Answerable int `json:"answerable"`
	// Ranked is Agreement over just those areas, and is the number to read when
	// judging a change to ranking. Measured on a real project the two differ by
	// more than forty points, because most declared owners had not committed
	// inside the window that was indexed.
	Ranked float64 `json:"ranked"`

	// People is how many person records exist after identity merging.
	People int `json:"people"`
	// Records is how many separate records those people arrived as, so the
	// difference between the two is what identity resolution actually did.
	Records int `json:"records"`
	// Joins is how many merges were inferred rather than proven by a shared
	// address or provider id.
	Joins int `json:"joins"`
	// Unlinked is how many handle-only records never found a person. Every one
	// is an owner who cannot agree and a body of work attributed to nobody, so
	// this is the ceiling on Agreement rather than a detail.
	Unlinked int `json:"unlinked"`

	// Topics is how many subjects the index holds, and Salient how many survive
	// the test for being a real subject rather than a fragment of one.
	Topics  int `json:"topics"`
	Salient int `json:"salient"`
	// Ties is how many connections between subjects were found, and Spans how
	// many of those rest on a single person.
	Ties int `json:"ties"`
	// Spans and Regions are the two knowledge-risk findings.
	Spans   int `json:"spans"`
	Regions int `json:"regions"`
	// Sources names what was indexed, sorted, so two results are only compared
	// when they are measuring the same thing.
	Sources []string `json:"sources"`
	// Areas is the verdict on each declared area, sorted by area. It is what
	// lets two runs be scored on the questions they can both answer, which is
	// the only comparison that means anything when the two indexes cover
	// different amounts of the organization.
	Areas []Area `json:"areas,omitempty"`
}

// Measure computes every number in one pass over the index.
func Measure(ix *index.Index) Result {
	var r Result
	own := resolve.Ownership(ix)
	for _, a := range resolve.OwnedAreas(ix) {
		r.Areas = append(r.Areas, Area{
			Topic: a.Topic, Held: a.Standing == resolve.StandingHeld, Answerable: a.Answerable(),
		})
	}
	r = Result{
		Areas:    r.Areas,
		Declared: own.Declared,
		Held:     own.Held,
		Silent:   own.Silent,
		Unworked: own.Unworked,
		Trailing: own.Trailing,
		Spans:    len(resolve.SoleSpans(ix, 0)),
		Regions:  len(resolve.Regions(ix, 0)),
		Joins:    len(ix.Joins()),
	}
	if own.Declared > 0 {
		r.Agreement = float64(own.Held) / float64(own.Declared)
	}
	// An owner with no work here at all, whether they are missing from the
	// window or own the area on paper only, is not a question about ranking.
	r.Answerable = own.Held + own.Trailing
	if r.Answerable > 0 {
		r.Ranked = float64(own.Held) / float64(r.Answerable)
	}

	// Canonicalize deletes a merged record from the graph and keeps its id on
	// the person it folded into, so counting the graph twice would report that
	// every record resolved to itself no matter how much merging happened. The
	// records that existed before merging are the people plus what they absorbed.
	for id, p := range ix.Graph.People {
		r.People++
		r.Records += 1 + len(p.Identities)
		if handleOnly(id) && len(p.Identities) == 0 {
			r.Unlinked++
		}
	}

	sources := make(map[string]bool)
	for _, t := range ix.Graph.Topics {
		r.Topics++
		if t.Salient() {
			r.Salient++
		}
		r.Ties += len(t.Near)
		for _, src := range t.Sources {
			sources[src] = true
		}
	}
	// Every tie is recorded from both ends, so counting them once is the honest
	// figure to compare against another run.
	r.Ties /= 2

	r.Sources = slices.Sorted(maps.Keys(sources))
	return r
}

// handleOnly reports whether an id is a source-prefixed handle, such as
// github:kim-doe, rather than an address or a name. It mirrors the rule the
// identity joiner uses, since this counts exactly what that failed to join.
func handleOnly(id model.ID) bool {
	s := string(id)
	i := strings.Index(s, ":")
	return i > 0 && !strings.Contains(s[i+1:], "@")
}

// Area is one declared area and whether whotoask named its owner of record.
type Area struct {
	// Topic is the area.
	Topic string `json:"topic"`
	// Held is whether the declared owner is also the person whotoask names.
	Held bool `json:"held"`
	// Answerable is whether the declared owner has recorded work in this area
	// at all. When they do not, no ranking could have named them.
	Answerable bool `json:"answerable"`
}

// Change is one number that moved between two runs.
type Change struct {
	// Name is the number that moved.
	Name string `json:"name"`
	// Before and After are its values.
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	// Better says which way the movement counts, and is false for numbers that
	// are context rather than score, such as how many topics were found.
	Better bool `json:"better"`
	// Scored says whether Better means anything for this number.
	Scored bool `json:"scored"`
}

// Delta is how far After moved from Before.
func (c Change) Delta() float64 { return c.After - c.Before }

// Compare reports what moved between two measurements, worst regression first,
// so a change that quietly cost accuracy to buy something else cannot hide at
// the bottom of a list.
//
// It refuses to compare results from different sources, because nearly every
// number here moves when a source is added and none of that movement is a
// change in quality.
func Compare(before, after Result) ([]Change, bool) {
	if strings.Join(before.Sources, ",") != strings.Join(after.Sources, ",") {
		return nil, false
	}
	// up says a rising number is an improvement; down, that a falling one is.
	const up, down, ctx = 1, -1, 0
	rows := []struct {
		Name string
		A, B float64
		Dir  int
	}{
		{"ranked", before.Ranked, after.Ranked, up},
		{"agreement", before.Agreement, after.Agreement, up},
		{"held", float64(before.Held), float64(after.Held), up},
		{"answerable", float64(before.Answerable), float64(after.Answerable), ctx},
		{"unlinked", float64(before.Unlinked), float64(after.Unlinked), down},
		{"silent", float64(before.Silent), float64(after.Silent), down},
		{"unworked", float64(before.Unworked), float64(after.Unworked), ctx},
		{"trailing", float64(before.Trailing), float64(after.Trailing), ctx},
		{"joins", float64(before.Joins), float64(after.Joins), ctx},
		{"people", float64(before.People), float64(after.People), ctx},
		{"declared", float64(before.Declared), float64(after.Declared), ctx},
		{"topics", float64(before.Topics), float64(after.Topics), ctx},
		{"salient", float64(before.Salient), float64(after.Salient), ctx},
		{"ties", float64(before.Ties), float64(after.Ties), ctx},
		{"spans", float64(before.Spans), float64(after.Spans), ctx},
		{"regions", float64(before.Regions), float64(after.Regions), ctx},
	}
	var out []Change
	for _, row := range rows {
		if row.A == row.B {
			continue
		}
		c := Change{Name: row.Name, Before: row.A, After: row.B, Scored: row.Dir != ctx}
		if c.Scored {
			c.Better = (row.B > row.A) == (row.Dir == up)
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Regressions first, then improvements, then everything unscored.
		ri, rj := regressed(out[i]), regressed(out[j])
		if ri != rj {
			return ri
		}
		return out[i].Scored && !out[j].Scored
	})
	// Within a group the order is the one the rows are declared in, which runs
	// headline first. Sorting by how far a number moved would be wrong: these
	// are different units, and a rate between zero and one always loses to a
	// count, so a six-point fall in agreement would sort below six more people.
	return out, true
}

// regressed reports whether a change made a scored number worse.
func regressed(c Change) bool { return c.Scored && !c.Better }

// Head is a comparison over only the areas both runs can answer.
//
// It exists because every score here is conditioned on what was indexed, so two
// runs over different coverage are answering different questions and their
// overall scores cannot be subtracted. Widening a history window on a real
// project moved Ranked from 72.1% to 57.1% while also raising the number of
// areas scored from 405 to 629, and reading that fall as worse ranking would
// have been wrong: the extra areas were the hard ones. This asks the only
// question that survives the difference, which is whether the same area got a
// better answer than it did before.
type Head struct {
	// Areas is how many areas both runs could answer.
	Areas int `json:"areas"`
	// BeforeHeld and AfterHeld are how many of those each run got right.
	BeforeHeld int `json:"beforeHeld"`
	AfterHeld  int `json:"afterHeld"`
	// Won are areas the later run got right and the earlier one did not, and
	// Lost the reverse. Both are listed, since which areas moved says more than
	// the count and a change that trades one set for another is not an
	// improvement even when the totals rise.
	Won  []string `json:"won,omitempty"`
	Lost []string `json:"lost,omitempty"`
}

// Rate is the share of shared areas the later run got right, and Was the share
// the earlier one did. Both are zero when nothing is shared.
func (h Head) Rate() float64 { return share(h.AfterHeld, h.Areas) }

// Was is the share the earlier run got right over the same areas.
func (h Head) Was() float64 { return share(h.BeforeHeld, h.Areas) }

// share divides safely, returning zero rather than a NaN when nothing was asked.
func share(n, of int) float64 {
	if of <= 0 {
		return 0
	}
	return float64(n) / float64(of)
}

// CompareAreas scores two runs over the areas both could answer. It reports
// false when neither run recorded its areas, which is the case for a baseline
// saved before this existed.
func CompareAreas(before, after Result) (Head, bool) {
	if len(before.Areas) == 0 || len(after.Areas) == 0 {
		return Head{}, false
	}
	was := make(map[string]Area, len(before.Areas))
	for _, a := range before.Areas {
		was[a.Topic] = a
	}
	var h Head
	for _, now := range after.Areas {
		// Only an area both runs could have got right is a fair question. An
		// area answerable in one run and not the other differs because of what
		// was indexed, which is the very thing being controlled for.
		then, ok := was[now.Topic]
		if !ok || !then.Answerable || !now.Answerable {
			continue
		}
		h.Areas++
		if then.Held {
			h.BeforeHeld++
		}
		if now.Held {
			h.AfterHeld++
		}
		switch {
		case now.Held && !then.Held:
			h.Won = append(h.Won, now.Topic)
		case then.Held && !now.Held:
			h.Lost = append(h.Lost, now.Topic)
		}
	}
	sort.Strings(h.Won)
	sort.Strings(h.Lost)
	return h, h.Areas > 0
}
