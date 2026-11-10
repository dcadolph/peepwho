package index

import (
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/model"
)

// CanonicalizeStore re-resolves every stored conversation's participants
// against the graph as it stands now. Identities join over time, so a
// conversation recorded before a person's handle was linked to their email
// would otherwise stay unreachable to them.
func (ix *Index) CanonicalizeStore(store *episode.Store) {
	for _, ep := range store.All() {
		// canonicalParticipants returns a fresh slice, so the stored episode's
		// own list is left intact for Relink to read the old identifiers from.
		// Reusing the episode's backing array here would let this overwrite the
		// keys Relink then needs to unlink, leaving stale reverse-index entries.
		next := ix.canonicalParticipants(ep.Participants)
		asked := ix.Canonical(ep.Asked)
		if !sameIDs(ep.Participants, next) || asked != ep.Asked {
			store.Relink(ep.ID, next, asked)
		}
	}
}

// sameIDs reports whether two participant lists match.
func sameIDs(a, b []model.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CanonicalizeEpisodes resolves each conversation's participants to the people
// the graph knows. A change reviewed under a GitHub login, a ticket closed
// under a Jira account, and a Slack thread all name the same person once this
// has run, which is what lets recall find a person's work wherever it
// happened. Participants that collapse onto one person are deduplicated, and
// the order they were seen in is kept.
func (ix *Index) CanonicalizeEpisodes(eps []episode.Episode) {
	for i := range eps {
		eps[i].Participants = ix.canonicalParticipants(eps[i].Participants)
		// The asker is resolved with them. Left behind, it holds a
		// source-native id while the participants hold canonical ones, matches
		// none of them, and Answered excludes nobody: the person who asked is
		// credited with answering their own question.
		eps[i].Asked = ix.Canonical(eps[i].Asked)
	}
}

// canonicalParticipants resolves a participant list to canonical identifiers in
// a fresh slice, dropping empties and duplicates and keeping first-seen order.
// It never reuses the input's backing array, so a caller still holding the
// original list, such as a stored episode about to be relinked, keeps reading
// its old identifiers.
func (ix *Index) canonicalParticipants(participants []model.ID) []model.ID {
	out := make([]model.ID, 0, len(participants))
	seen := make(map[model.ID]bool, len(participants))
	for _, p := range participants {
		id := ix.Canonical(p)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
