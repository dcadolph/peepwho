package simorg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/recall"
	"github.com/dcadolph/peepwho/internal/resolve"
	"github.com/dcadolph/peepwho/internal/slack"
)

// Built is a generated company after the real pipeline has run over it: the
// index and conversations peepwho would have from those sources, alongside the
// answers the company was built to have.
type Built struct {
	// Org is the generated company.
	Org *Org
	// Index is the graph the connectors produced.
	Index *index.Index
	// Episodes are the conversations the connectors produced.
	Episodes *episode.Store
}

// Embed fills the index and episode vectors through a local model, exactly as
// `peepwho index --embed` would, so semantic mode can be scored against the
// same questions keyword mode was.
func (b *Built) Embed(ctx context.Context, embedder index.Embedder) error {
	if err := b.Index.Embed(ctx, embedder); err != nil {
		return fmt.Errorf("simorg: embed index: %w", err)
	}
	for _, ep := range b.Episodes.All() {
		vec, err := embedder.Embed(ctx, ep.Text())
		if err != nil {
			return fmt.Errorf("simorg: embed episode: %w", err)
		}
		b.Episodes.SetVector(ep.ID, vec)
	}
	return nil
}

// ScoreSemantic asks questions of one kind through semantic mode, which ranks
// by meaning rather than by shared words. It is what the blind questions
// exist to measure.
func (b *Built) ScoreSemantic(ctx context.Context, embedder resolve.Embedder, kind Kind, limit int) Score {
	res := resolve.NewSemantic(b.Index, embedder)
	var score Score
	for _, q := range b.Org.Questions {
		if q.Kind != kind {
			continue
		}
		score.Asked++
		ans, err := res.Resolve(ctx, q.Text, limit)
		if err != nil {
			score.record(q.Text, 0)
			continue
		}
		rank := 0
		for i, m := range ans.People {
			if m.Person != nil && m.Person.ID == q.WantPerson {
				rank = i + 1
				break
			}
		}
		score.record(q.Text, rank)
	}
	return score
}

// Build runs the real ingest path over a generated company. It uses the same
// connectors, the same indexer, and the same episode store the binary uses, so
// what is measured afterwards is peepwho rather than a stand-in for it.
func Build(spec Spec, dir string) (*Built, error) {
	org := Generate(spec)
	defer org.Close()
	return BuildFrom(org, dir, spec)
}

// BuildFrom runs the ingest pipeline over an already generated company. Two
// calls over one company are two ingests of one input, which is what the
// reproducibility claim is about; generating twice stamps new message times
// and is two inputs.
func BuildFrom(org *Org, dir string, spec Spec) (*Built, error) {
	csvPath := filepath.Join(dir, "org.csv")
	if err := os.WriteFile(csvPath, []byte(org.CSV), 0o600); err != nil {
		return nil, fmt.Errorf("simorg: write org csv: %w", err)
	}

	ctx := context.Background()
	ix := index.New()

	csvRecs, err := connector.NewOrgCSV(csvPath).Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("simorg: org csv: %w", err)
	}
	ix.Add(csvRecs)

	src := connector.NewSlackWithClient(
		slack.New("xoxb-generated", slack.WithBaseURL(org.Slack.URL)),
		connector.SlackOptions{Episodes: true, Archive: !spec.NoArchive, MaxMessages: 100000})
	slackRecs, err := src.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("simorg: slack: %w", err)
	}
	ix.Add(slackRecs)
	ix.AutoJoin()
	ix.Canonicalize()

	eps := src.Episodes()
	ix.CanonicalizeEpisodes(eps)
	store := episode.New()
	for _, ep := range eps {
		store.Add(ep)
	}
	return &Built{Org: org, Index: ix, Episodes: store}, nil
}

// Score is how well peepwho answered a set of questions with known answers.
type Score struct {
	// Asked is how many questions were put to it.
	Asked int
	// Top1 is how many were answered correctly by the first result.
	Top1 int
	// Top3 is how many had the right answer in the first three.
	Top3 int
	// Missed lists questions whose answer never appeared, worst first, capped
	// so a failure prints something a person can read.
	Missed []string
	// ReciprocalRank sums 1/rank over every question, which rewards being
	// close when it is not exactly right.
	ReciprocalRank float64
}

// Precision1 is the share answered correctly by the first result.
func (s Score) Precision1() float64 { return ratio(s.Top1, s.Asked) }

// Precision3 is the share whose answer appeared in the first three.
func (s Score) Precision3() float64 { return ratio(s.Top3, s.Asked) }

// MRR is the mean reciprocal rank, the usual single number for ranking
// quality: 1.0 means every answer came first, 0.5 means second on average.
func (s Score) MRR() float64 { return ratio64(s.ReciprocalRank, s.Asked) }

// String renders a score for a test log.
func (s Score) String() string {
	return fmt.Sprintf("asked=%d p@1=%.2f p@3=%.2f mrr=%.2f", s.Asked, s.Precision1(), s.Precision3(), s.MRR())
}

// ratio divides safely.
func ratio(n, d int) float64 { return ratio64(float64(n), d) }

// ratio64 divides safely.
func ratio64(n float64, d int) float64 {
	if d == 0 {
		return 0
	}
	return n / float64(d)
}

// maxMissed caps how many failures a score records, so a bad run reports a
// readable sample instead of thousands of lines.
const maxMissed = 8

// ScoreWhoKnows asks every who-knows question and scores where the owner
// ranked. The owner is the only right answer by construction: they are the
// only person made fluent in that subject.
func (b *Built) ScoreWhoKnows(limit int) Score { return b.scoreKind(KindWhoKnows, limit) }

// ScoreAnchored scores questions asked the way a person asks months later:
// one word of the subject remembered, the rest in their own words.
func (b *Built) ScoreAnchored(limit int) Score { return b.scoreKind(KindAnchored, limit) }

// ScoreBlind scores questions that share no vocabulary with the subject at
// all. Word matching cannot win these, and the number is here to size honestly
// how much a model adds rather than to be passed.
func (b *Built) ScoreBlind(limit int) Score { return b.scoreKind(KindBlind, limit) }

// scoreKind asks every question of one kind and scores where the owner ranked.
func (b *Built) scoreKind(kind Kind, limit int) Score {
	res := resolve.NewKeyword(b.Index)
	var score Score
	for _, q := range b.Org.Questions {
		if q.Kind != kind {
			continue
		}
		score.Asked++
		ans, err := res.Resolve(context.Background(), q.Text, limit)
		if err != nil {
			score.record(q.Text, 0)
			continue
		}
		rank := 0
		for i, m := range ans.People {
			if m.Person != nil && m.Person.ID == q.WantPerson {
				rank = i + 1
				break
			}
		}
		score.record(q.Text, rank)
	}
	return score
}

// ScoreChannelRouting asks every question with a known right place to ask,
// and scores where that channel ranked among the channels the answer offers.
// Sending somebody to the wrong channel wastes a whole team's attention, not
// one person's, so routing is scored on its own rather than inferred from the
// people ranking.
func (b *Built) ScoreChannelRouting(limit int) Score {
	res := resolve.NewKeyword(b.Index)
	var score Score
	for _, q := range b.Org.Questions {
		if q.WantChannel == "" {
			continue
		}
		score.Asked++
		ans, err := res.Resolve(context.Background(), q.Text, limit)
		if err != nil {
			score.record(q.Text, 0)
			continue
		}
		rank := 0
		for i, m := range ans.Channels {
			if m.Channel != nil && m.Channel.Name == q.WantChannel {
				rank = i + 1
				break
			}
		}
		score.record(q.Text, rank)
	}
	return score
}

// ScoreRecall asks each thread's own asker about the problem they raised, and
// scores where that conversation ranked. Nobody else is asked, because recall
// only ever answers about a person's own conversations.
func (b *Built) ScoreRecall(limit int) Score {
	res := recall.New(b.Episodes, b.Index)
	var score Score
	for _, q := range b.Org.Questions {
		if q.Kind != KindRecall {
			continue
		}
		score.Asked++
		ans := res.Resolve(context.Background(),
			recall.Query{Text: q.Text, Person: q.Asker, Limit: limit})
		rank := 0
		for i, ep := range ans.Episodes {
			if episodeMatches(ep, q) {
				rank = i + 1
				break
			}
		}
		score.record(q.Text, rank)
	}
	return score
}

// episodeMatches reports whether a returned conversation is the exact one the
// question was planted from, by its stable id. Matching the id rather than just
// the helper who took part is a stricter, less gameable bar: with several
// conversations sharing a helper, the looser check passed without surfacing the
// right one. It falls back to the helper only when no planted id is recorded.
func episodeMatches(ep recall.Episode, q Question) bool {
	if q.WantEpisode != "" {
		return ep.ID == q.WantEpisode
	}
	for _, p := range ep.People {
		if model.ID(p.Email) == q.WantPerson {
			return true
		}
	}
	return false
}

// record folds one result into a score. A rank of zero means the answer never
// appeared at all.
func (s *Score) record(question string, rank int) {
	switch {
	case rank == 1:
		s.Top1++
		s.Top3++
		s.ReciprocalRank++
	case rank > 0 && rank <= 3:
		s.Top3++
		s.ReciprocalRank += 1 / float64(rank)
	case rank > 3:
		s.ReciprocalRank += 1 / float64(rank)
	default:
		if len(s.Missed) < maxMissed {
			s.Missed = append(s.Missed, question)
		}
	}
}
