// Package index builds an on-disk, searchable map of expertise from connector
// records and ranks people and channels for a query without an LLM.
package index

import (
	"maps"
	"math"
	"slices"
	"sync/atomic"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/identity"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// DefaultHalfLife is the age at which a dated record's weight halves.
//
// Measured, not chosen. Swept against home-assistant/core with everything else
// held fixed, so all five runs answered the same 1,100 declared areas over the
// same 68,142 subjects, scored on how often the declared owner of an area is
// also the person peepwho names (see `peepwho eval` and CONTRIBUTING):
//
//	30 days   64.8%
//	90 days   71.0%
//	180 days  72.0%   <- here
//	365 days  72.3%
//	no decay  71.5%
//
// The suspicion worth recording is the one this killed: that ranking favours
// whoever contributed most over whoever maintains an area now, and that a
// shorter half-life would correct it. It does not. Leaning harder on recency
// costs seven points, because how much someone has done in an area is most of
// what makes them the person to ask. The curve is flat from 90 days out, so
// there is nothing to win here; spend the effort on identity linkage, where
// most disagreements actually come from.
const DefaultHalfLife = 180 * 24 * time.Hour

// Field weights scale how strongly each signal contributes to a score. An
// explicit topic or a channel name outweighs a title word, which outweighs a
// team word, which outweighs free text.
const (
	// weightTopic is the affinity weight of an explicit topic tag.
	weightTopic = 3.0
	// weightChannelName is the affinity weight of the channel's own name.
	weightChannelName = 3.0
	// weightTitle is the affinity weight of a job-title word.
	weightTitle = 2.0
	// weightTeam is the affinity weight of a team-name word.
	weightTeam = 1.0
	// weightText is the affinity weight of a free-text word.
	weightText = 0.5
)

// Evidence strengths grade how convincing a matched field is when estimating
// strength: an explicit topic is proof, a passing mention is a hint.
const (
	// evidenceTopic is the strength of an explicit topic or channel-name hit.
	evidenceTopic = 1.0
	// evidenceTitle is the strength of a job-title hit.
	evidenceTitle = 0.85
	// evidenceTeam is the strength of a team-name hit.
	evidenceTeam = 0.7
	// evidenceMention is the strength of a free-text mention.
	evidenceMention = 0.5
)

// personText holds the normalized field text for a person, used to explain why
// the person matched a query.
type personText struct {
	// Titles are the lowercased job titles seen across sources, accumulated so a
	// title a later record lacks is not lost to last-write-wins.
	Titles []string `json:"titles"`
	// Teams are the lowercased team names seen across sources.
	Teams []string `json:"teams"`
	// Topics are the lowercased explicit topic names.
	Topics []string `json:"topics"`
	// Text is the accumulated lowercased free text. It is readable message
	// content used only in memory, to build embeddings and to merge two people
	// on a join, so it is never written to disk.
	Text string `json:"-"`
}

// channelText holds the normalized field text for a channel, used to explain
// why the channel matched a query.
type channelText struct {
	// Name is the lowercased channel name.
	Name string `json:"name"`
	// Topic is the lowercased channel topic.
	Topic string `json:"topic"`
	// Topics are the lowercased explicit topic names.
	Topics []string `json:"topics"`
}

// Index is a searchable expertise index over people and channels.
type Index struct {
	// Graph is the entity graph this index was built from.
	Graph *model.Graph
	// postings maps a token to per-person accumulated weight.
	postings map[string]map[model.ID]float64
	// texts holds normalized per-person field text for reason lookup.
	texts map[model.ID]*personText
	// channelPostings maps a token to per-channel accumulated weight.
	channelPostings map[string]map[model.ID]float64
	// channelTexts holds normalized per-channel field text for reason lookup.
	channelTexts map[model.ID]*channelText
	// personVecs holds per-person embedding vectors when present.
	personVecs map[model.ID][]float32
	// channelVecs holds per-channel embedding vectors when present.
	channelVecs map[model.ID][]float32
	// topicVecs holds per-topic embedding vectors when present. A topic vector
	// describes a subject rather than a person, which is what a question
	// paraphrased in someone's own words is really about.
	topicVecs map[model.ID][]float32
	// resolver maps the identifiers a person accumulates across sources to one
	// canonical identifier.
	resolver *identity.Resolver
	// joins records the inferred identity merges AutoJoin made, with the
	// strength and evidence for each.
	joins []Join
	// halfLife is the age at which a dated record's weight halves; zero or
	// negative disables recency decay.
	halfLife time.Duration
	// now returns the current time; tests pin it for deterministic decay.
	now func() time.Time
	// fbRules are preprocessed user votes applied during ranking, held
	// atomically so a running server can apply new votes mid-flight.
	fbRules atomic.Pointer[[]fbRule]
	// fbStep is the per-vote score multiplier; zero means the default.
	fbStep float64
	// fbMax clamps net votes per result; zero means the default, negative off.
	fbMax int
	// embedProgress, when set, is called after each entity is embedded.
	embedProgress util.Progress
	// sources holds the raw records per source, needed only to rebuild on a
	// merge. It is nil in an index loaded to answer a query, since the records
	// live in a sidecar file that queries never read. A non-nil map marks an
	// index whose sources are in hand and safe to save back to the sidecar.
	sources map[string][]connector.Record
	// sourceCounts is how many records each source contributed, kept in the main
	// index so status can report it without loading the sidecar.
	sourceCounts map[string]int
	// builtAt is when a loaded index was last written, empty for a fresh one.
	builtAt time.Time
	// personLens and channelLens cache BM25 document lengths, refreshed when
	// postings change so a query never rescans every posting.
	personLens  entityLens
	channelLens entityLens
	// personVocab and channelVocab bucket posting keys by length so a fuzzy term
	// scans only candidates within its edit-distance band, not the whole
	// vocabulary.
	personVocab  vocabIndex
	channelVocab vocabIndex
}

// New returns an empty index with initialized maps. Of the options only
// WithNow applies; the codec matters only to Load and Save.
func New(opts ...Option) *Index {
	cfg := newIOConfig(opts)
	ix := &Index{
		Graph:           model.NewGraph(),
		postings:        make(map[string]map[model.ID]float64),
		texts:           make(map[model.ID]*personText),
		channelPostings: make(map[string]map[model.ID]float64),
		channelTexts:    make(map[model.ID]*channelText),
		personVecs:      make(map[model.ID][]float32),
		channelVecs:     make(map[model.ID][]float32),
		topicVecs:       make(map[model.ID][]float32),
		resolver:        identity.NewResolver(),
		halfLife:        DefaultHalfLife,
		now:             cfg.now,
	}
	ix.refreshStats()
	return ix
}

// refreshStats recomputes the cached length tables and vocabulary buckets from
// the current postings. It runs after any operation that changes postings, so
// serving reads never rescan the full posting set. It must not run concurrently
// with a search, matching the index's build-then-serve lifecycle.
func (ix *Index) refreshStats() {
	ix.personLens = lengthsOf(ix.postings)
	ix.channelLens = lengthsOf(ix.channelPostings)
	ix.personVocab = newVocabIndex(ix.postings)
	ix.channelVocab = newVocabIndex(ix.channelPostings)
}

// SetHalfLife sets the age at which a dated record's weight halves. Zero or
// negative disables recency decay.
func (ix *Index) SetHalfLife(d time.Duration) { ix.halfLife = d }

// decay returns the recency multiplier for a record dated t: one for undated
// records, future dates, or disabled decay, halving per half-life of age.
func (ix *Index) decay(t time.Time) float64 {
	if t.IsZero() || ix.halfLife <= 0 {
		return 1
	}
	// Age is measured from the top of the day, not the instant of the build.
	// Half-lives are weeks to months, so a sub-day difference moves no
	// ranking, and with the instant in the exponent two builds of one input a
	// second apart differed in the last bits of every weight. Reproducibility
	// is a product claim: the same input must build the same index, byte for
	// byte, all day.
	age := ix.now().UTC().Truncate(24 * time.Hour).Sub(t)
	if age <= 0 {
		return 1
	}
	return math.Exp2(-float64(age) / float64(ix.halfLife))
}

// BuiltAt reports when a loaded index was last written, so a caller can show how
// stale it is. It is the zero time for an index that has never been saved.
func (ix *Index) BuiltAt() time.Time { return ix.builtAt }

// PostingCount returns the number of distinct terms in the person index, a
// measure of the searchable vocabulary size.
func (ix *Index) PostingCount() int { return len(ix.postings) }

// SourceNames returns the names of the sources that contributed to the index,
// sorted, so a status view can list each one with its size. It reads the counts
// kept in the main index, so it works without loading the sources sidecar.
func (ix *Index) SourceNames() []string {
	names := slices.Sorted(maps.Keys(ix.sourceCounts))
	return names
}

// SourceSize reports how many records the named source contributed to this
// index, so a caller can tell a full read from a truncated one. It reads the
// count kept in the main index, so it works without loading the sources sidecar.
func (ix *Index) SourceSize(name string) int { return ix.sourceCounts[name] }

// LoadAliases merges a JSON alias file into the index's identity resolver so
// records indexed afterward key by their canonical identifier. Call
// Canonicalize to also join people already in the graph.
func (ix *Index) LoadAliases(path string) error {
	return ix.identityResolver().LoadFile(path)
}

// Alias records that two identifiers belong to the same person. Records
// indexed afterward key by the canonical identifier; call Canonicalize to
// also join people already in the graph.
func (ix *Index) Alias(a, b model.ID) {
	ix.identityResolver().Union(a, b)
}

// identityResolver returns the index's resolver, initializing it on first use.
func (ix *Index) identityResolver() *identity.Resolver {
	if ix.resolver == nil {
		ix.resolver = identity.NewResolver()
	}
	return ix.resolver
}

// Canonical resolves an identifier a source used, such as a Slack user ID or
// a GitHub login, to the person it belongs to. An identifier the index has
// never seen comes back unchanged.
func (ix *Index) Canonical(id model.ID) model.ID {
	return ix.identityResolver().Canonical(id)
}
