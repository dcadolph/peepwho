package resolve

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/index"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
)

// placeBreadthExp is the exponent breadth is discounted by in a place score.
// Chosen on kubernetes/test-infra and validated on kubernetes/kubernetes
// against their own OWNERS files: half over-punished the maintainers who work
// across a whole tree, which on a large project is exactly who the owners
// are, and zero hands every place to raw volume.
const placeBreadthExp = 0.25

// PlaceHolder is one person a place's work rests on.
type PlaceHolder struct {
	// ID is the person's canonical identifier.
	ID string `json:"id"`
	// Name is their display name.
	Name string `json:"name"`
	// Work is their commit-and-credit count in the place.
	Work float64 `json:"work"`
	// Share is their part of the place's credited work, over every holder and
	// not only the ones listed, so the listed shares need not sum to one.
	Share float64 `json:"share"`
}

// Place is one directory and the people its work rests on.
type Place struct {
	// Dir is the directory relative to the repository root.
	Dir string `json:"dir"`
	// Work is the total credited work under it.
	Work float64 `json:"work"`
	// People is how many have worked there at all, before the list of holders
	// is cut down, so a place with one holder listed out of twenty is not read
	// as a place resting on one person.
	People int `json:"people"`
	// Bus is how many of them it takes to cover the concentration cut, by the
	// same rule the topic findings use. One means the place rests on one
	// person whatever the head count says.
	Bus int `json:"bus"`
	// Holders are the people it rests on, strongest first.
	Holders []PlaceHolder `json:"holders"`
}

// reviewWeight is what one review of a pull request contributes to holding
// the places it changed, relative to a commit landing there. A review is real
// evidence of holding an area and weaker evidence than changing it, and on
// projects where approval is decoupled from authorship it is the only
// evidence the record carries about the people who approve.
const reviewWeight = 0.5

// MostFragile reorders places by how exposed they are rather than how large,
// and keeps the first n.
//
// PlaceLeads ranks by volume, which is right for a deliverable listing the
// systems a business runs on. It is wrong for a risk view: the largest
// directories are the ones most people have touched, so ranking by size sorts
// the least concentrated work to the top and buries every finding underneath
// it. Fewest holders first, and among equally held places the busiest, so what
// leads is the place one person holds and everybody depends on.
func MostFragile(places []Place, n int) []Place {
	out := make([]Place, len(places))
	copy(out, places)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bus != out[j].Bus {
			return out[i].Bus < out[j].Bus
		}
		return out[i].Work > out[j].Work
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// AddReviewCredit folds review participation into a place tally: everyone who
// took part in a pull request is credited, at review weight, with the
// directories that pull request changed. The two inputs come from different
// places and neither costs an extra request: the git history knows which
// merge landed which pull request, and the forge knows who took part in it.
//
// People are keyed by "github:login" so the identity join resolves them to
// the same person their commits belong to. The tally is modified in place and
// returned for convenience.
func AddReviewCredit(
	dirWork map[string]map[string]float64, workTotals map[string]float64,
	pullDirs map[connector.PullRef][]string, pullPeople map[connector.PullRef][]string,
) map[string]map[string]float64 {
	if dirWork == nil {
		dirWork = make(map[string]map[string]float64)
	}
	byNumber := unambiguousNumbers(pullPeople)
	for pull, dirs := range pullDirs {
		people := reviewersOf(pullPeople, byNumber, pull)
		if len(people) == 0 || len(dirs) == 0 {
			continue
		}
		for _, login := range people {
			if login == "" {
				continue
			}
			key := "github:" + strings.ToLower(login)
			for _, dir := range dirs {
				m := dirWork[dir]
				if m == nil {
					m = make(map[string]float64)
					dirWork[dir] = m
				}
				m[key] += reviewWeight
			}
			workTotals[key] += reviewWeight
		}
	}
	return dirWork
}

// reviewersOf finds who took part in one pull request.
//
// The exact reference wins. A clone that could not say which repository it is,
// because it has no remote or its remote does not name a forge repository,
// yields a reference with no repository, and that falls back to matching on
// the number alone. The fallback is taken only where the number belongs to
// exactly one repository, because a number claimed by two is precisely the
// collision that once credited one repository's reviewers into another's
// directories, with the winner decided by which worker finished first.
func reviewersOf(
	pullPeople map[connector.PullRef][]string, byNumber map[int][]string,
	pull connector.PullRef,
) []string {
	if people := pullPeople[pull]; len(people) > 0 {
		return people
	}
	if pull.Repo != "" {
		return nil
	}
	return byNumber[pull.Number]
}

// unambiguousNumbers indexes reviewers by pull request number, keeping only
// the numbers that belong to a single repository.
func unambiguousNumbers(pullPeople map[connector.PullRef][]string) map[int][]string {
	repos := make(map[int]map[string]bool, len(pullPeople))
	for ref := range pullPeople {
		if repos[ref.Number] == nil {
			repos[ref.Number] = make(map[string]bool)
		}
		repos[ref.Number][ref.Repo] = true
	}
	out := make(map[int][]string, len(pullPeople))
	for ref, people := range pullPeople {
		if len(repos[ref.Number]) == 1 {
			out[ref.Number] = people
		}
	}
	return out
}

// PlaceLeads ranks, for every directory in a git connector's place tally, the
// people its work rests on: work there discounted by breadth, identities
// folded to their canonical person through the index. Subjects answer what
// somebody knows; this answers what a place rests on, which is what an
// ownership question asks. Directories with less work than minWork are
// dropped, and each keeps at most k holders.
func PlaceLeads(
	ix *index.Index, dirWork map[string]map[string]float64, workTotals map[string]float64,
	minWork float64, k int,
) []Place {
	emailIdx := placeEmailIndex(ix)
	var out []Place
	for dir, people := range dirWork {
		var total float64
		byCanon := make(map[model.ID]*PlaceHolder)
		// Walked in address order. One person holds several tally keys, so
		// their discounted work is a sum of quotients that are not exactly
		// representable, and float addition is not associative: taken in Go's
		// randomized map order the last bits differ between runs, and two
		// people whose work is inexactly equal then break their tie
		// differently. Measured on kubernetes that moved a scored directory,
		// and every place in an assessment is ranked by this number.
		for _, email := range slices.Sorted(maps.Keys(people)) {
			w := people[email]
			total += w
			breadth := workTotals[email]
			if breadth <= 0 {
				continue
			}
			canon := resolvePlacePerson(ix, emailIdx, email)
			h := byCanon[canon]
			if h == nil {
				name := string(canon)
				if p := ix.Graph.People[canon]; p != nil && p.Name != "" {
					name = p.Name
				}
				h = &PlaceHolder{ID: string(canon), Name: name}
				byCanon[canon] = h
			}
			h.Work += w / math.Pow(breadth, placeBreadthExp)
		}
		if total < minWork || len(byCanon) == 0 {
			continue
		}
		holders := make([]PlaceHolder, 0, len(byCanon))
		for _, h := range byCanon {
			holders = append(holders, *h)
		}
		sort.Slice(holders, func(i, j int) bool {
			if holders[i].Work != holders[j].Work {
				return holders[i].Work > holders[j].Work
			}
			return holders[i].Name < holders[j].Name
		})
		// Shares are worked out against the discounted total over every
		// holder, before the list is cut to k. Dividing by Work instead would
		// mix the two scales: Work counts commits, holder work counts commits
		// after the breadth discount, and the ratio would mean nothing.
		var held float64
		for _, h := range holders {
			held += h.Work
		}
		bus := 0
		var cum float64
		for i := range holders {
			if held > 0 {
				holders[i].Share = holders[i].Work / held
			}
			if cum < concentrationCut {
				cum += holders[i].Share
				bus++
			}
		}
		people := len(holders)
		if len(holders) > k {
			holders = holders[:k]
		}
		out = append(out, Place{
			Dir: dir, Work: total, People: people, Bus: bus, Holders: holders,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Work != out[j].Work {
			return out[i].Work > out[j].Work
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

// placeEmailIndex maps every address and identity the graph knows to its
// canonical person.
func placeEmailIndex(ix *index.Index) map[string]model.ID {
	out := make(map[string]model.ID)
	for id, p := range ix.Graph.People {
		canon := ix.CanonicalID(id)
		out[strings.ToLower(string(id))] = canon
		if p.Email != "" {
			out[strings.ToLower(p.Email)] = canon
		}
		for _, alt := range p.Identities {
			out[strings.ToLower(string(alt))] = canon
		}
	}
	return out
}

// resolvePlacePerson folds one tally email to its canonical person, through
// the graph first and a noreply login second.
func resolvePlacePerson(ix *index.Index, emailIdx map[string]model.ID, email string) model.ID {
	key := strings.ToLower(email)
	if canon, ok := emailIdx[key]; ok {
		return canon
	}
	if login, ok := util.GitHubNoreplyLogin(email); ok {
		if canon, ok := emailIdx["github:"+strings.ToLower(login)]; ok {
			return canon
		}
	}
	return ix.CanonicalID(model.ID(key))
}

// FuseRanks merges rankings by reciprocal rank, so a person one signal sees
// and another cannot still surfaces: the place ranking reads commits, the
// subject ranking also carries review-sourced weight, and an approver who
// only reviews exists in exactly one of them.
func FuseRanks(k int, rankings ...[]string) []string {
	const damp = 2.0
	scores := make(map[string]float64)
	order := make(map[string]int)
	for _, ranking := range rankings {
		for i, name := range ranking {
			if _, ok := order[name]; !ok {
				order[name] = len(order)
			}
			scores[name] += 1.0 / (damp + float64(i))
		}
	}
	names := make([]string, 0, len(scores))
	for n := range scores {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if scores[names[i]] != scores[names[j]] {
			return scores[names[i]] > scores[names[j]]
		}
		return order[names[i]] < order[names[j]]
	})
	if len(names) > k {
		names = names[:k]
	}
	return names
}
