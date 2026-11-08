package simorg

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// topicSlug is the path- and repo-friendly form of a subject name.
func topicSlug(s int) string { return strings.ReplaceAll(subjects[s].Topic, " ", "-") }

// topicWord is the leading word of a subject, used as a label and project key.
func topicWord(s int) string { return strings.Fields(subjects[s].Topic)[0] }

// crossing is a pair of subjects that some of the company's work spans, and
// the person who does that work.
type crossing struct {
	// A and B are the two subject indexes.
	A, B int
	// Who does the work that touches both, and is therefore the only person in
	// the company who knows the two belong together.
	Who person
}

// crossCuts pairs neighboring subjects and names one person who works across
// each pair.
//
// Real work crosses areas: a change to billing touches the ledger, and an issue
// about search is filed against indexing too. Without that the generated company
// is a set of sealed cells, nothing is ever worked on alongside anything else,
// and the connections between subjects have nothing to be found in. Every pair
// is worked by a single person on purpose, because a connection only one person
// has ever made is the finding that no per-subject report can show.
func (c *company) crossCuts() []crossing {
	out := make([]crossing, 0, len(c.owners))
	for s := 0; s+1 < len(c.owners); s += 2 {
		out = append(out, crossing{A: s, B: s + 1, Who: c.owners[s].who})
	}
	// The subjects one person holds are also joined to each other, so they form
	// a body of work and not two unconnected pairs. Without this the company
	// has nothing for the joined-work finding to report, and the demo shows an
	// empty panel where the heaviest thing peepwho says belongs.
	for s, held := range heldTogether {
		if s > 0 && held < len(c.owners) && s < len(c.owners) {
			out = append(out, crossing{A: s - 1, B: s, Who: c.owners[held].who})
		}
	}
	return out
}

// baseItems is how much tracked work each subject carries in the sources that
// cost nothing to generate, and basePages how many pages describe it. Both sit
// well above crossingItems so a subject's own owner always does the most of it
// and stays the one right answer, which is the whole basis of the gauntlet.
const (
	baseItems = 10
	basePages = 3
)

// crossingItems is how many separate pieces of work span each pair. It has to
// clear the floor below which a pairing is treated as coincidence, and stay well
// under what a subject's own owner does in it, so working across a boundary
// never makes somebody look like the owner of the far side.
const crossingItems = 3

// experts returns everyone made fluent in subject s, owner first, so a source can
// spread its activity across the same people the Slack history made expert.
func (c *company) experts(s int) []person {
	out := []person{c.owners[s].who}
	for i := range c.people {
		if c.people[i].email == c.owners[s].who.email {
			continue
		}
		for _, t := range c.people[i].topics {
			if t == s {
				out = append(out, c.people[i])
				break
			}
		}
	}
	return out
}

// jsonHandler serves a fixed value as JSON.
func jsonHandler(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, v) }
}

// githubServer serves one repository per subject in GitHub's wire format, owned by
// that subject's experts, and resolves every login to an email so the GitHub
// identity joins the same person in Slack and the org chart. It returns the repo
// list the connector should read.
func (c *company) githubServer() (*httptest.Server, []string) {
	mux := http.NewServeMux()
	var repos []string
	logins := map[string]person{}

	crossing := c.crossCuts()
	for s := range c.owners {
		exp := c.experts(s)
		name := topicSlug(s)
		full := "corp/" + name
		repos = append(repos, full)

		mux.HandleFunc("/repos/"+full, jsonHandler(map[string]any{
			"name": name, "full_name": full,
			"description": topicSlug(s) + " service", "topics": []string{topicSlug(s)},
		}))

		contribs := make([]map[string]any, 0, len(exp))
		for j, p := range exp {
			if p.github == "" {
				continue
			}
			logins[p.github] = p
			contribs = append(contribs, map[string]any{"login": p.github, "contributions": 40 - j*8})
		}
		mux.HandleFunc("/repos/"+full+"/contributors", jsonHandler(contribs))

		pulls := make([]map[string]any, 0, 3)
		if exp[0].github != "" {
			for k := range 3 {
				num := s*10 + k
				pulls = append(pulls, map[string]any{
					"number":     num,
					"html_url":   fmt.Sprintf("https://github.com/%s/pull/%d", full, num),
					"title":      fmt.Sprintf("fix %s regression %d", topicSlug(s), k+1),
					"user":       map[string]any{"login": exp[0].github},
					"labels":     []map[string]any{{"name": topicSlug(s)}},
					"updated_at": isoDaysAgo(3 + k), "merged_at": isoDaysAgo(3 + k),
				})
			}
		}
		// Pull requests opened against two areas at once, by the one person who
		// works across the boundary.
		for _, x := range crossing {
			if x.A != s || x.Who.github == "" {
				continue
			}
			for k := range crossingItems {
				num := s*10 + 900 + k
				pulls = append(pulls, map[string]any{
					"number":   num,
					"html_url": fmt.Sprintf("https://github.com/%s/pull/%d", full, num),
					"title":    fmt.Sprintf("reconcile %s %d", topicSlug(x.A), k+1),
					"user":     map[string]any{"login": x.Who.github},
					"labels": []map[string]any{
						{"name": topicSlug(x.A)}, {"name": topicSlug(x.B)},
					},
					"updated_at": isoDaysAgo(4 + k), "merged_at": isoDaysAgo(4 + k),
				})
			}
		}
		mux.HandleFunc("/repos/"+full+"/pulls", jsonHandler(pulls))
		mux.HandleFunc("/repos/"+full+"/issues", jsonHandler([]map[string]any{}))
	}

	for login, p := range logins {
		lp := p
		mux.HandleFunc("/users/"+login, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"login": lp.github, "name": lp.name, "email": lp.email})
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	return httptest.NewServer(mux), repos
}

// jiraServer serves resolved issues in Jira Cloud's wire format, each closed by the
// subject's owner, so tickets add expertise and recall.
func (c *company) jiraServer() *httptest.Server {
	issues := make([]map[string]any, 0, len(c.owners)*3)
	for s := range c.owners {
		exp := c.experts(s)
		word := topicWord(s)
		proj := strings.ToUpper(word)
		if len(proj) > 4 {
			proj = proj[:4]
		}
		for k := range baseItems {
			// Rotate the work across everyone fluent in the subject, so a topic
			// with several experts reads as shared rather than concentrated, but
			// keep the owner in half the slots so the person who holds a subject
			// still plainly does the most of it.
			owner := exp[0]
			if k%2 == 1 {
				owner = exp[(k/2+1)%len(exp)]
			}
			issues = append(issues, map[string]any{
				"key": fmt.Sprintf("%s-%d", proj, s*10+k),
				"fields": map[string]any{
					"summary": fmt.Sprintf("%s issue %d", topicSlug(s), k+1),
					"labels":  []string{topicSlug(s)},
					"project": map[string]any{"key": proj, "name": topicSlug(s)},
					"updated": jiraDaysAgo(5 + k),
					"assignee": map[string]any{
						"accountId": "j-" + owner.id, "displayName": owner.name, "emailAddress": owner.email,
					},
					"resolutiondate": jiraDaysAgo(5 + k),
					"status": map[string]any{
						"name": "Done", "statusCategory": map[string]any{"key": "done"},
					},
				},
			})
		}
	}
	// Tickets filed against two areas at once, each closed by the one person who
	// works across the boundary.
	for _, x := range c.crossCuts() {
		proj := strings.ToUpper(topicWord(x.A))
		if len(proj) > 4 {
			proj = proj[:4]
		}
		for k := range crossingItems {
			issues = append(issues, map[string]any{
				"key": fmt.Sprintf("%s-%d", proj, x.A*10+90+k),
				"fields": map[string]any{
					// Only one subject is named in prose. Two names side by
					// side read as a compound subject of their own, and the
					// demo filled up with subjects nobody works on. The labels
					// are what say the ticket spans both.
					"summary": fmt.Sprintf("%s mismatch %d", topicSlug(x.A), k+1),
					"labels":  []string{topicSlug(x.A), topicSlug(x.B)},
					"project": map[string]any{"key": proj, "name": topicSlug(x.A)},
					"updated": jiraDaysAgo(6 + k),
					"assignee": map[string]any{
						"accountId": "j-" + x.Who.id, "displayName": x.Who.name,
						"emailAddress": x.Who.email,
					},
					"resolutiondate": jiraDaysAgo(6 + k),
					"status": map[string]any{
						"name": "Done", "statusCategory": map[string]any{"key": "done"},
					},
				},
			})
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/api/3/search/jql", jsonHandler(map[string]any{"issues": issues, "isLast": true}))
	return httptest.NewServer(mux)
}

// confluenceServer serves one runbook per subject in Confluence v2's wire format,
// authored by the subject's owner.
func (c *company) confluenceServer() *httptest.Server {
	type page struct {
		id, title, space, author string
		labels                   []string
		ago                      int
	}
	pages := make([]page, 0, len(c.owners))
	spaceName := map[string]string{}
	users := map[string]map[string]any{}
	for s := range c.owners {
		owner := c.owners[s].who
		sp := "sp-" + owner.id
		spaceName[sp] = topicSlug(s)
		aid := "c-" + owner.id
		users[aid] = map[string]any{"accountId": aid, "displayName": owner.name, "email": owner.email}
		for k := range basePages {
			pages = append(pages, page{
				fmt.Sprintf("%d-%d", s, k),
				fmt.Sprintf("%s runbook %d", topicSlug(s), k+1),
				sp, aid, []string{topicSlug(s)}, 6 + s,
			})
		}
	}
	// Pages written about two areas at once, by the person who works across them.
	for _, x := range c.crossCuts() {
		sp, aid := "sp-"+x.Who.id, "c-"+x.Who.id
		users[aid] = map[string]any{
			"accountId": aid, "displayName": x.Who.name, "email": x.Who.email,
		}
		for k := range crossingItems {
			pages = append(pages, page{
				fmt.Sprintf("x%d-%d", x.A, k),
				fmt.Sprintf("%s handoff %d", topicSlug(x.A), k+1),
				sp, aid, []string{topicSlug(x.A), topicSlug(x.B)}, 7 + x.A,
			})
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/rest/api/user/current", jsonHandler(map[string]any{"accountId": "c-me", "email": "demo@example.com"}))
	mux.HandleFunc("/wiki/rest/api/user", func(w http.ResponseWriter, r *http.Request) {
		if u, ok := users[r.URL.Query().Get("accountId")]; ok {
			writeJSON(w, u)
			return
		}
		writeJSON(w, map[string]any{"accountId": r.URL.Query().Get("accountId")})
	})
	mux.HandleFunc("/wiki/api/v2/spaces/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/spaces/")
		writeJSON(w, map[string]any{"id": id, "key": id, "name": spaceName[id]})
	})
	mux.HandleFunc("/wiki/api/v2/pages", func(w http.ResponseWriter, _ *http.Request) {
		results := make([]any, 0, len(pages))
		for _, p := range pages {
			results = append(results, map[string]any{
				"id": p.id, "title": p.title, "spaceId": p.space, "authorId": p.author,
				"createdAt": isoDaysAgo(p.ago),
				"version":   map[string]any{"authorId": p.author, "createdAt": isoDaysAgo(p.ago)},
			})
		}
		writeJSON(w, map[string]any{"results": results, "_links": map[string]any{}})
	})
	mux.HandleFunc("/wiki/api/v2/pages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/pages/"), "/labels")
		labels := make([]any, 0)
		for _, p := range pages {
			if p.id == id {
				for _, l := range p.labels {
					labels = append(labels, map[string]any{"name": l})
				}
			}
		}
		writeJSON(w, map[string]any{"results": labels})
	})
	return httptest.NewServer(mux)
}

// pagerdutyServer serves services, resolved incidents, and on-calls in PagerDuty's
// wire format for the first several subjects, owned by their experts.
func (c *company) pagerdutyServer() *httptest.Server {
	var services, incidents, oncalls []map[string]any
	for s := 0; s < len(c.owners) && s < 8; s++ {
		exp := c.experts(s)
		owner := exp[0]
		sid, epid, pid := fmt.Sprintf("S%d", s), fmt.Sprintf("EP%d", s), "P"+owner.id
		svcName := topicSlug(s)
		services = append(services, map[string]any{
			"id": sid, "name": svcName, "description": topicSlug(s),
			"escalation_policy": map[string]any{"id": epid},
		})
		oncalls = append(oncalls, map[string]any{
			"user":              map[string]any{"id": pid, "name": owner.name, "email": owner.email},
			"escalation_policy": map[string]any{"id": epid},
		})
		for k := range 2 {
			// A rotation means incidents land on different responders.
			resp := exp[k%len(exp)]
			id := fmt.Sprintf("PINC%d%d", s, k)
			incidents = append(incidents, map[string]any{
				"id": id, "incident_number": s*100 + k, "status": "resolved",
				"title":      fmt.Sprintf("%s degraded", topicSlug(s)),
				"html_url":   "https://corp.pagerduty.com/incidents/" + id,
				"created_at": isoDaysAgo(20 + k), "resolved_at": isoDaysAgo(20 + k),
				"service": map[string]any{"id": sid, "summary": svcName},
				"assignments": []map[string]any{
					{"assignee": map[string]any{
						"id": "P" + resp.id, "name": resp.name, "email": resp.email}},
				},
			})
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/services", jsonHandler(map[string]any{"more": false, "services": services}))
	mux.HandleFunc("/incidents", jsonHandler(map[string]any{"more": false, "incidents": incidents}))
	mux.HandleFunc("/oncalls", jsonHandler(map[string]any{"more": false, "oncalls": oncalls}))
	return httptest.NewServer(mux)
}

// buildGitRepo writes a repository under dir with a subject's owner committing to
// that subject's path, so authorship and recency line up with the rest.
func (c *company) buildGitRepo(dir string) error {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return fmt.Errorf("simorg: git init: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("simorg: git worktree: %w", err)
	}
	// A commit may touch more than one path, which is the whole of what makes
	// two subjects visibly belong together.
	commitAll := func(content, name, email string, when time.Time, rels ...string) error {
		for _, rel := range rels {
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				return err
			}
			if _, err := wt.Add(rel); err != nil {
				return err
			}
		}
		sig := &object.Signature{Name: name, Email: email, When: when}
		// Only the first path is named. A message listing both would put two
		// subject names side by side, and adjacent words register as a compound
		// subject of their own.
		_, err := wt.Commit(rels[0], &git.CommitOptions{Author: sig, Committer: sig})
		return err
	}
	commit := func(rel, content, name, email string, when time.Time) error {
		return commitAll(content, name, email, when, rel)
	}
	now := time.Now()
	for s := range c.owners {
		exp := c.experts(s)
		// The directory carries the subject; the filename must not carry a
		// second one. A real extension maps to a technology topic (.tf means
		// terraform), so every subject sharing one filename would make the
		// whole company expert in it.
		// "main" is a generic path segment the connector already ignores, and the
		// file carries no extension, so the directory is the only thing this
		// path says: the subject. Anything else, a real extension or a novel
		// stem, becomes a topic of its own held by everyone who ever committed.
		rel := topicSlug(s) + "/main"
		for k := range 3 {
			owner := exp[k%len(exp)]
			if err := commit(rel, fmt.Sprintf("v%d", k), owner.name, owner.email,
				now.AddDate(0, 0, -(s*3+k+1))); err != nil {
				return fmt.Errorf("simorg: git commit: %w", err)
			}
		}
	}
	for _, x := range c.crossCuts() {
		a, b := topicSlug(x.A)+"/main", topicSlug(x.B)+"/main"
		for k := range crossingItems {
			if err := commitAll(fmt.Sprintf("across%d", k), x.Who.name, x.Who.email,
				now.AddDate(0, 0, -(x.A*3+k+2)), a, b); err != nil {
				return fmt.Errorf("simorg: git commit: %w", err)
			}
		}
	}
	return nil
}
