// Scoring answer-hood: crediting the people who replied in a conversation with
// the subjects of the conversation they replied in.

package index

import (
	"maps"
	"slices"

	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/text"
)

// answerWeight is what answering in one conversation contributes to holding its
// subjects, against the weight a record that changed something contributes.
//
// It is the same 0.5 the place model already gives a review, taken deliberately
// rather than fitted: answering is real evidence somebody knew the area and
// weaker evidence than changing it, which is the identical argument that set
// the review weight. Choosing it by trying values against the OWNERS benchmark
// would make every later score on that benchmark a measurement of the tuning.
//
// The value has never been worth tuning, because at this weight the scoring
// changes nothing to tune. Measured 2026-09-15 on kubernetes/test-infra and
// kubernetes/kubernetes, crediting answer-hood moved one judged directory out
// of 198, inside the run-to-run wobble. See the note on ScoreAnswers.
const answerWeight = 0.5

// AnswerCredit reports what one ScoreAnswers pass folded in, so a run that
// credited nothing is visible as such rather than looking like a change that
// did not help.
type AnswerCredit struct {
	// Episodes is how many conversations carried an asker and at least one
	// answerer, which are the only ones that can credit anybody.
	Episodes int
	// Credits is how many person-and-subject increments were folded in.
	Credits int
	// Unknown names the answerers the graph does not hold, sorted. They earn
	// nothing, because inventing a person from an identifier no source
	// introduced would put a name in the index that no answer can stand behind.
	Unknown []string
}

// ScoreAnswers credits everyone who answered a conversation with that
// conversation's subjects, and credits nobody for having asked.
//
// Activity says a person was present. An answer says they knew: somebody put a
// question up and this person settled it. The episode store already separates
// the two, and until this ran nothing scored on the distinction.
//
// Asking earns nothing here. A person who opens a pull request or reports a bug
// has shown they met the area, not that they hold it, and they keep whatever
// their commits and their other answers already earned them elsewhere.
//
// Both Topics and Direct are credited. Ownership reads Direct and falls back to
// Topics only for a person who has no Direct at all, so crediting Topics alone
// would make answer-hood invisible to exactly the people whose commits gave
// them Direct, which is the maintainers. The confusion Direct exists to prevent
// does not arise: the subjects here are the episode's own title and place,
// stated by the source, not a path somewhere else that happens to carry an
// area's name.
//
// Call it after the identities are joined, so an answer given under a GitHub
// login lands on the person their commits belong to.
//
// Nothing in the product calls this. It is reached only from
// `peepwho eval owners --answer-topics`, and it is kept because the measurement
// it failed is worth being able to repeat rather than because it earns its
// place. Measured 2026-09-15 against the OWNERS files of kubernetes/test-infra
// and kubernetes/kubernetes: 842 and 340 episodes credited, 29,743 and 14,572
// subject increments folded in, and the ownership score moved by one judged
// directory out of 198, which is inside the wobble of scoring a loaded index
// twice. That is the same answer measurement two already reported for review
// credit placed on words rather than places, and it did not change because the
// words were re-attributed to the people who answered: a change episode's
// subjects on this corpus are its pull request title, and a pull request title
// carries no ownership signal whoever is credited with it.
func (ix *Index) ScoreAnswers(eps []episode.Episode) AnswerCredit {
	var out AnswerCredit
	unknown := make(map[string]bool)
	for i := range eps {
		ep := &eps[i]
		answered := ep.Answered()
		subjects := text.Tokenize(ep.Text())
		if len(answered) == 0 || len(subjects) == 0 {
			continue
		}
		out.Episodes++
		decay := ix.decay(ep.Occurred)
		for _, who := range answered {
			p := ix.Graph.People[ix.CanonicalID(ix.Canonical(who))]
			if p == nil {
				unknown[string(who)] = true
				continue
			}
			out.Credits += ix.creditSubjects(p, subjects, ep.Source, weightTopic*answerWeight*decay)
		}
	}
	out.Unknown = slices.Sorted(maps.Keys(unknown))
	return out
}

// creditSubjects folds one answerer's weight onto each subject, registering any
// subject the graph has not met yet as uncurated: a word mined from a title is
// not a subject somebody declared, and the salience bar decides whether it ever
// counts as one. It returns how many increments were folded.
func (ix *Index) creditSubjects(p *model.Person, subjects []string, source string, w float64) int {
	n := 0
	for _, subject := range subjects {
		tid := topicID(subject)
		if tid == "" {
			continue
		}
		noteTopic(ix.Graph, tid, subject, source, false)
		if p.Topics == nil {
			p.Topics = make(map[model.ID]float64)
		}
		if p.Direct == nil {
			p.Direct = make(map[model.ID]float64)
		}
		p.Topics[tid] += w
		p.Direct[tid] += w
		n++
	}
	return n
}
