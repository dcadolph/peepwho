package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/policy"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// feedbackStrengths maps the feedback presets to a per-vote multiplier and a
// net-vote clamp.
var feedbackStrengths = map[string]struct {
	// Step is the per-vote score multiplier.
	Step float64
	// MaxNet clamps net votes per result; negative disables feedback.
	MaxNet int
}{
	"off":    {Step: 1, MaxNet: -1},
	"low":    {Step: 1.1, MaxNet: 2},
	"normal": {Step: 1.25, MaxNet: 3},
	"high":   {Step: 1.5, MaxNet: 4},
}

// Engine answers questions from one opened index under one egress policy.
type Engine struct {
	// ix is the opened index every answer reads.
	ix *index.Index
	// pol gates every request that could leave the machine.
	pol policy.Policy
}

// New returns an engine over ix under pol. A nil index is a programming error.
func New(ix *index.Index, pol policy.Policy) *Engine {
	if ix == nil {
		panic("engine.New: index required")
	}
	return &Engine{ix: ix, pol: pol}
}

// Index returns the opened index.
func (e *Engine) Index() *index.Index { return e.ix }

// Policy returns the egress policy the engine runs under.
func (e *Engine) Policy() policy.Policy { return e.pol }

// SetFeedback puts the votes in store onto the ranking.
func (e *Engine) SetFeedback(store *feedback.Store) {
	e.ix.SetFeedback(store.All())
}

// SetFeedbackStrength sets how hard votes move ranking: off, low, normal, or
// high. Empty means normal.
func (e *Engine) SetFeedbackStrength(name string) error {
	if name == "" {
		name = "normal"
	}
	s, ok := feedbackStrengths[name]
	if !ok {
		return fmt.Errorf("%w: feedback strength %q (want off, low, normal, or high)", ErrBadArgs, name)
	}
	e.ix.SetFeedbackStrength(s.Step, s.MaxNet)
	return nil
}

// Resolver builds the resolver cfg selects over this engine's index and policy.
func (e *Engine) Resolver(cfg ResolverConfig) (resolve.Resolver, error) {
	return NewResolver(e.ix, e.pol, cfg)
}

// Ask answers query with up to limit results per section, using the resolver
// cfg selects.
func (e *Engine) Ask(ctx context.Context, query string, limit int, cfg ResolverConfig) (resolve.Answer, error) {
	res, err := e.Resolver(cfg)
	if err != nil {
		return resolve.Answer{}, err
	}
	return res.Resolve(ctx, query, limit)
}

// Search finds people and channels by name, email, title, team, or topic.
func (e *Engine) Search(query string, limit int) []resolve.SearchResult {
	return resolve.Search(e.ix, query, limit)
}

// Person returns the profile of the person with canonical id, and false when
// no such person is indexed.
func (e *Engine) Person(id string) (resolve.JSONProfile, bool) {
	profile, ok := e.ix.Profile(model.ID(id))
	if !ok {
		return resolve.JSONProfile{}, false
	}
	return resolve.ProfileView(e.ix, profile), true
}

// Recaller returns the resolver that finds past conversations in store,
// scoped per person against this engine's index.
func (e *Engine) Recaller(store *episode.Store) *recall.Resolver {
	return recall.New(store, e.ix)
}

// Status describes what the index holds and how fresh it is.
type Status struct {
	// People is the number of people indexed.
	People int
	// Channels is the number of channels indexed.
	Channels int
	// Teams is the number of teams indexed.
	Teams int
	// Subjects counts topics a source stated.
	Subjects int
	// Words counts topics mined from prose.
	Words int
	// Sources maps each source name to the records it contributed.
	Sources map[string]int
	// Embeddings reports whether the index carries vectors for semantic mode.
	Embeddings bool
	// BuiltAt is when the index was last written, zero when never saved.
	BuiltAt time.Time
	// Age is how long before now the index was written, zero when never saved.
	Age time.Duration
}

// Status reports what the index holds, with its age measured from now. The
// caller supplies now so a host process owns the clock.
func (e *Engine) Status(now time.Time) Status {
	// A subject was stated by a source; a word was mined from prose so a
	// question phrased in somebody's own vocabulary still matches. The two are
	// counted apart because the words outnumber the subjects by orders of
	// magnitude and a single total misrepresents both.
	stated := 0
	for _, t := range e.ix.Graph.Topics {
		if t.Curated {
			stated++
		}
	}
	sources := make(map[string]int)
	for _, name := range e.ix.SourceNames() {
		sources[name] = e.ix.SourceSize(name)
	}
	st := Status{
		People:     len(e.ix.Graph.People),
		Channels:   len(e.ix.Graph.Channels),
		Teams:      len(e.ix.Graph.Teams),
		Subjects:   stated,
		Words:      len(e.ix.Graph.Topics) - stated,
		Sources:    sources,
		Embeddings: e.ix.HasEmbeddings(),
	}
	if t := e.ix.BuiltAt(); !t.IsZero() {
		st.BuiltAt = t
		st.Age = now.Sub(t)
	}
	return st
}
