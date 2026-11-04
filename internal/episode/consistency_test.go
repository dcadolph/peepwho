package episode

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/dcadolph/peepwho/internal/model"
)

// TestStoreConsistencyUnderRandomOperations drives a store through thousands
// of random adds, replacements, relinks, purges, and vector writes, checking
// after every operation that the derived indexes agree exactly with the
// episodes held. A single stale posting or participant link would surface a
// deleted conversation in search results, so this is the invariant the whole
// privacy story rests on.
func TestStoreConsistencyUnderRandomOperations(t *testing.T) {
	t.Parallel()
	for _, seed := range []int64{1, 7, 42, 1234, 99999} {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(seed))
			s := New()
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ids := []string{"e1", "e2", "e3", "e4", "e5", "e6", "e7", "e8"}
			people := []model.ID{"ana", "bo", "cy", "dee", "ed", "fay"}
			words := []string{"billing", "retries", "kafka", "deploy", "outage", "schema", "quota"}

			randPeople := func() []model.ID {
				n := 1 + rng.Intn(3)
				out := make([]model.ID, 0, n+1)
				for range n {
					out = append(out, people[rng.Intn(len(people))])
				}
				if rng.Intn(4) == 0 {
					// A participant listed twice, which a source with a sloppy
					// membership list can produce, must not corrupt the links.
					out = append(out, out[0])
				}
				return out
			}
			randEpisode := func() Episode {
				ep := Episode{
					ID:           ids[rng.Intn(len(ids))],
					Source:       "slack",
					Kind:         "thread",
					Place:        "#" + words[rng.Intn(len(words))],
					Title:        words[rng.Intn(len(words))] + " " + words[rng.Intn(len(words))],
					Participants: randPeople(),
					Occurred:     base.AddDate(0, 0, rng.Intn(200)),
					Body:         words[rng.Intn(len(words))] + " " + words[rng.Intn(len(words))],
				}
				if rng.Intn(3) == 0 {
					ep.Archive = []Note{{Author: ep.Participants[0], At: ep.Occurred, Text: "we fixed it"}}
				}
				return ep
			}

			for op := range 2000 {
				switch rng.Intn(7) {
				case 0, 1, 2:
					s.Add(randEpisode())
				case 3:
					s.SetVector(ids[rng.Intn(len(ids))], []float32{rng.Float32(), rng.Float32()})
				case 4:
					s.Relink(ids[rng.Intn(len(ids))], randPeople(), "")
				case 5:
					if rng.Intn(4) == 0 {
						s.PurgeBefore(base.AddDate(0, 0, rng.Intn(200)))
					} else {
						s.PurgeArchive()
					}
				case 6:
					q := Query{Text: words[rng.Intn(len(words))], Person: people[rng.Intn(len(people))]}
					for _, r := range s.Search(q) {
						if got, ok := s.Episode(r.Episode.ID); !ok || got != r.Episode {
							t.Fatalf("op %d: search returned episode %q not in the store", op, r.Episode.ID)
						}
						if !r.Episode.Involves(q.Person) {
							t.Fatalf("op %d: search scoped to %q returned episode %q without them",
								op, q.Person, r.Episode.ID)
						}
					}
				}
				checkConsistent(t, s, op)
				if t.Failed() {
					return
				}
			}
		})
	}
}

// checkConsistent asserts every derived index agrees exactly with the episodes
// held: no posting or link points at a missing episode, every participant link
// is mirrored in the episode, and every episode is reachable from each of its
// participants.
func checkConsistent(t *testing.T, s *Store, op int) {
	t.Helper()
	for term, posting := range s.postings {
		if len(posting) == 0 {
			t.Fatalf("op %d: term %q kept an empty posting map", op, term)
		}
		for id := range posting {
			if _, ok := s.episodes[id]; !ok {
				t.Fatalf("op %d: term %q posts to missing episode %q", op, term, id)
			}
		}
	}
	for p, epIDs := range s.byParticipant {
		if len(epIDs) == 0 {
			t.Fatalf("op %d: person %q kept an empty episode list", op, p)
		}
		for _, id := range epIDs {
			ep, ok := s.episodes[id]
			if !ok {
				t.Fatalf("op %d: person %q links to missing episode %q", op, p, id)
			}
			if !ep.Involves(p) {
				t.Fatalf("op %d: person %q links to episode %q that does not list them", op, p, id)
			}
		}
	}
	for id, ep := range s.episodes {
		for _, p := range ep.Participants {
			found := false
			for _, linked := range s.byParticipant[p] {
				if linked == id {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("op %d: episode %q lists %q but is not reachable from them", op, id, p)
			}
		}
		if s.HasPerson("") {
			t.Fatalf("op %d: empty person reports episodes", op)
		}
	}
	for id := range s.vecs {
		if _, ok := s.episodes[id]; !ok {
			t.Fatalf("op %d: vector kept for missing episode %q", op, id)
		}
	}
	if all := s.All(); len(all) != s.Len() {
		t.Fatalf("op %d: All() returned %d episodes, Len() reports %d", op, len(all), s.Len())
	} else {
		for i := 1; i < len(all); i++ {
			if all[i-1].Occurred.Before(all[i].Occurred) {
				t.Fatalf("op %d: All() not ordered newest first at %d", op, i)
			}
		}
	}
}
