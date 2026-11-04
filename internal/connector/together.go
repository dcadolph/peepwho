package connector

import (
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/util"
)

// Bounds on what counts as two subjects being worked on together. They are
// shared by every source so a tie means the same thing wherever it came from.
const (
	// maxTogether is the most subjects one piece of work may touch and still
	// say anything about them. A rename swept across two hundred areas does not
	// make those areas one body of knowledge, and neither does an issue tagged
	// with everything.
	maxTogether = 18
	// minTogether is how many separate pieces of work must have paired two
	// subjects before the pairing is more than a coincidence.
	minTogether = 3
	// minSubjectItems is how often a subject must appear at all before its ties
	// mean anything.
	minSubjectItems = 5
	// ubiquitousShare is the share of work above which a subject is the
	// scaffolding everything touches rather than a subject of its own, and so
	// is tied to everything by construction.
	ubiquitousShare = 0.35
	// ubiquitousFloor is the amount of history below which that share means
	// nothing. In a young project a real subject is genuinely touched by half
	// of it, and calling that scaffolding would leave nothing tied to anything.
	ubiquitousFloor = 200
	// maxLinks bounds how many ties one subject keeps, strongest first.
	maxLinks = 12
	// scaffoldShare is the share of the tied vocabulary a subject may be tied to
	// before it is scaffolding rather than a subject.
	//
	// It is the same rule as the graph's ubiquity check, measured against the
	// vocabulary instead of the people, because the people version cannot see
	// this. On a real issue tracker the label a bot puts on every ticket with a
	// patch is held by a sixth of the contributors, nowhere near the share of
	// people that marks scaffolding, while being tied to seventy per cent of
	// every subject in the tracker. What gives it away is its reach across the
	// vocabulary, not how many people carry it.
	scaffoldShare = 0.35
	// scaffoldFloor is the vocabulary below which that share means nothing:
	// among a handful of subjects everything is tied to most of the rest.
	scaffoldFloor = 20
	// spreadShare is the share of a source's containers a subject may turn up
	// in before it is a kind of work rather than an area. A container is
	// whatever the source divides work into: a Jira project, a repository, a
	// wiki space.
	//
	// Reach across the vocabulary catches the labels tied to everything, and
	// misses the ones that are merely everywhere. Measured on a real tracker,
	// documentation and sql have almost the same number of ties and the same
	// neighbourhood shape, and no measure of the tie graph tells them apart.
	// What does is that sql lives in two projects and documentation lives in
	// all five: it means the same thing everywhere, which is what a kind of
	// work is and what an area never is.
	spreadShare = 0.5
	// spreadFloor is how many containers a source must have before that share
	// says anything. One repository cannot show a subject staying inside it.
	spreadFloor = 3
	// minLinkWeight only guards against arithmetic noise. It is deliberately
	// almost zero: how much of the time two subjects move as one thing is a
	// tiny number whenever both are also worked on alone, which is normal, and
	// a floor set by eye cuts the real ties along with the noise. What makes a
	// tie trustworthy is minTogether, several separate pieces of work, and what
	// bounds the result is maxLinks keeping only the strongest.
	minLinkWeight = 0.001
)

// processLabels are the labels that describe what is being done to a piece of
// work rather than what it is about. They form no concept ties.
//
// This is a list rather than a rule because it has to be. Four structural
// measures were tried against a real tracker and every one failed: how many
// subjects a label is tied to, how tightly its neighbours cluster, how far its
// neighbourhood falls apart without it, and how concentrated its experts are.
// On all four, "sql" and "shuffle" score as process and "build" scores as an
// area. What separates them is what the words mean, and no shape in the graph
// carries that.
//
// It stays deliberately short and holds only what is process on every tracker:
// the labels GitHub creates in a new repository, and the handful of tracker
// conventions that mean the same thing wherever they appear. A label somebody
// invented for their own project is not in here and cannot be.
//
// Only ties are affected. Somebody can still be the person who knows the
// documentation; documentation just does not make two areas related.
var processLabels = map[string]bool{
	// The labels GitHub puts in every new repository.
	"bug": true, "documentation": true, "duplicate": true, "enhancement": true,
	"good first issue": true, "good-first-issue": true, "help wanted": true,
	"help-wanted": true, "invalid": true, "question": true, "wontfix": true,
	// Conventions that mean the same thing on any tracker.
	"beginner": true, "docs": true, "easyfix": true, "feature-request": true,
	"needs-triage": true, "newbie": true, "patch-available": true,
	"pull-request-available": true, "regression": true, "release-notes": true,
	"releasenotes": true, "stale": true, "starter": true, "triage": true,
	"wip": true, "work-in-progress": true,
}

// subjectPair is two subject names in a fixed order, so a pairing is counted
// once however the two were encountered.
type subjectPair struct {
	// A is the alphabetically first subject.
	A string
	// B is the other one.
	B string
}

// pairOf orders two subjects into a pair.
func pairOf(a, b string) subjectPair {
	if a > b {
		a, b = b, a
	}
	return subjectPair{A: a, B: b}
}

// togetherIndex accumulates which subjects are worked on together, and who has
// worked across each pairing. It is the one thing a source can say about a
// subject that does not run through the people who hold it: two areas changed
// in the same commit, or fixed in the same issue, are related whoever did it.
//
// Every source feeds the same accumulator so a tie carries the same meaning
// wherever it came from. A source with units of work that name several subjects
// at once, which is most of them, calls note; git pairs across paths itself and
// calls begin and pair, because two spellings of one path are not two subjects
// meeting.
type togetherIndex struct {
	// items is how many pieces of work have been read, the denominator for
	// deciding which subjects are scaffolding.
	items int
	// seen counts the pieces of work each subject appeared in.
	seen map[string]int
	// pairs counts the pieces of work in which two subjects appeared at once.
	pairs map[subjectPair]int
	// spanned records who has worked across each pairing, which is what tells a
	// connection everybody makes from one only ever made by a single person.
	spanned map[subjectPair]map[string]bool
	// standalone holds the subjects that name something on their own rather than
	// only ever appearing as a word inside a longer name.
	standalone map[string]bool
	// places records the containers each subject turned up in, and everywhere
	// holds all of them, so a subject that is the same thing in every corner of
	// the company can be told from one that belongs to a corner.
	places     map[string]map[string]bool
	everywhere map[string]bool
}

// newTogether returns an empty accumulator.
func newTogether() *togetherIndex {
	return &togetherIndex{
		seen:       make(map[string]int),
		pairs:      make(map[subjectPair]int),
		spanned:    make(map[subjectPair]map[string]bool),
		standalone: make(map[string]bool),
		places:     make(map[string]map[string]bool),
		everywhere: make(map[string]bool),
	}
}

// begin records one piece of work and the distinct subjects it named. It
// reports whether those subjects are worth pairing: fewer than two say nothing
// about each other, and too many say nothing at all.
func (t *togetherIndex) begin(subjects map[string]bool, where string) bool {
	t.items++
	if where != "" {
		t.everywhere[where] = true
	}
	for s := range subjects {
		t.seen[s]++
		if where == "" {
			continue
		}
		in := t.places[s]
		if in == nil {
			in = make(map[string]bool)
			t.places[s] = in
		}
		in[where] = true
	}
	return len(subjects) >= 2 && len(subjects) <= maxTogether
}

// standing records the subjects that name something of their own: a directory
// by that name, a component, a label. A source that has no such notion records
// nothing, and the rule that uses this stays quiet.
func (t *togetherIndex) standing(names []string) {
	for _, n := range names {
		if n != "" {
			t.standalone[n] = true
		}
	}
}

// pair records that one person worked across two subjects in one piece of work.
func (t *togetherIndex) pair(a, b, who string) {
	p := pairOf(a, b)
	t.pairs[p]++
	by := t.spanned[p]
	if by == nil {
		by = make(map[string]bool)
		t.spanned[p] = by
	}
	if who != "" {
		by[who] = true
	}
}

// note records one piece of work that named several subjects at once, such as
// an issue, a page, or a thread. Everything it named met everything else it
// named, which is what makes those sources simpler than a repository: there are
// no directories above the work to mistake for the work itself.
func (t *togetherIndex) note(subjects []string, who, where string) {
	// Normalized here rather than by each caller: a source that hands over
	// "Matter" and one that hands over "matter" mean the same subject, and
	// counting them apart would split a pairing below the floor that makes it
	// trustworthy.
	distinct := make(map[string]bool, len(subjects))
	for _, s := range subjects {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			distinct[s] = true
		}
	}
	if !t.begin(distinct, where) {
		return
	}
	names := make([]string, 0, len(distinct))
	for s := range distinct {
		names = append(names, s)
	}
	sort.Strings(names)
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if util.SameFamily(names[i], names[j]) {
				continue
			}
			t.pair(names[i], names[j], who)
		}
	}
}

// records renders the accumulated ties as one record per subject, naming what
// it is worked on alongside and how many people have ever worked across the two.
func (t *togetherIndex) records(source string) []Record {
	if t.items == 0 {
		return nil
	}
	ceiling := float64(t.items) * ubiquitousShare
	if t.items < ubiquitousFloor {
		ceiling = float64(t.items)
	}
	links := make(map[string][]TopicLink)
	for p, n := range t.pairs {
		if n < minTogether {
			continue
		}
		// Two words of one compound name are not two subjects meeting. The three
		// words of data_grand_lyon appear together in every file of that one
		// integration, which says how it is spelled and nothing else. A subject
		// that names a directory of its own is a real subject wherever else it
		// turns up, so one standing side is enough to keep the pairing.
		if len(t.standalone) > 0 && !t.standalone[p.A] && !t.standalone[p.B] {
			continue
		}
		na, nb := t.seen[p.A], t.seen[p.B]
		if na < minSubjectItems || nb < minSubjectItems {
			continue
		}
		if float64(na) > ceiling || float64(nb) > ceiling {
			continue
		}
		// How much of the time the two move as one thing.
		weight := float64(n) / float64(na+nb-n)
		if weight < minLinkWeight {
			continue
		}
		by := t.spanned[p]
		sole := ""
		if len(by) == 1 {
			for who := range by {
				sole = who
			}
		}
		links[p.A] = append(links[p.A],
			TopicLink{To: p.B, Weight: weight, Witnesses: len(by), Sole: sole})
		links[p.B] = append(links[p.B],
			TopicLink{To: p.A, Weight: weight, Witnesses: len(by), Sole: sole})
	}
	for name := range links {
		if processLabels[name] {
			delete(links, name)
		}
	}
	for name, ties := range links {
		kept := ties[:0]
		for _, tie := range ties {
			if !processLabels[tie.To] {
				kept = append(kept, tie)
			}
		}
		if len(kept) == 0 {
			delete(links, name)
			continue
		}
		links[name] = kept
	}
	links = dropScaffolding(links)
	links = t.dropSpread(links)

	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Record, 0, len(names))
	for _, name := range names {
		ties := links[name]
		sort.Slice(ties, func(i, j int) bool {
			if ties[i].Weight != ties[j].Weight {
				return ties[i].Weight > ties[j].Weight
			}
			return ties[i].To < ties[j].To
		})
		if len(ties) > maxLinks {
			ties = ties[:maxLinks]
		}
		out = append(out, Record{Kind: KindTopic, Source: source, Name: name, Links: ties})
	}
	return out
}

// absorb folds another accumulator into this one, which is how the workers of a
// parallel walk combine what each of them saw without sharing a map.
func (t *togetherIndex) absorb(o *togetherIndex) {
	if o == nil {
		return
	}
	t.items += o.items
	for s, n := range o.seen {
		t.seen[s] += n
	}
	for p, n := range o.pairs {
		t.pairs[p] += n
	}
	for n := range o.standalone {
		t.standalone[n] = true
	}
	for s, in := range o.places {
		into := t.places[s]
		if into == nil {
			into = make(map[string]bool)
			t.places[s] = into
		}
		for where := range in {
			into[where] = true
		}
	}
	for where := range o.everywhere {
		t.everywhere[where] = true
	}
	for p, by := range o.spanned {
		into := t.spanned[p]
		if into == nil {
			into = make(map[string]bool)
			t.spanned[p] = into
		}
		for who := range by {
			into[who] = true
		}
	}
}

// dropScaffolding removes the subjects that reach across the vocabulary rather
// than sitting somewhere in it, and takes them out of everything they were tied
// to. A subject tied to most of what a company works on is describing the kind
// of work rather than the area, and it makes every subject look adjacent to
// every other.
func dropScaffolding(links map[string][]TopicLink) map[string][]TopicLink {
	if len(links) < scaffoldFloor {
		return links
	}
	cut := float64(len(links)) * scaffoldShare
	scaffold := make(map[string]bool)
	for name, ties := range links {
		if float64(len(ties)) > cut {
			scaffold[name] = true
		}
	}
	if len(scaffold) == 0 {
		return links
	}
	out := make(map[string][]TopicLink, len(links)-len(scaffold))
	for name, ties := range links {
		if scaffold[name] {
			continue
		}
		kept := make([]TopicLink, 0, len(ties))
		for _, t := range ties {
			if !scaffold[t.To] {
				kept = append(kept, t)
			}
		}
		if len(kept) > 0 {
			out[name] = kept
		}
	}
	return out
}

// dropSpread removes the subjects that turn up in most of a source's containers.
// A subject that means the same thing in every project, repository, or space is
// naming a kind of work rather than an area, and tying areas to it says only
// that both had documentation written about them.
func (t *togetherIndex) dropSpread(links map[string][]TopicLink) map[string][]TopicLink {
	if len(t.everywhere) < spreadFloor {
		return links
	}
	cut := float64(len(t.everywhere)) * spreadShare
	spread := make(map[string]bool)
	for name := range links {
		if float64(len(t.places[name])) > cut {
			spread[name] = true
		}
	}
	if len(spread) == 0 {
		return links
	}
	out := make(map[string][]TopicLink, len(links)-len(spread))
	for name, ties := range links {
		if spread[name] {
			continue
		}
		kept := make([]TopicLink, 0, len(ties))
		for _, tie := range ties {
			if !spread[tie.To] {
				kept = append(kept, tie)
			}
		}
		if len(kept) > 0 {
			out[name] = kept
		}
	}
	return out
}
