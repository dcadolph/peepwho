package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/resolve"
	"github.com/dcadolph/whotoask/internal/seal"
	"github.com/dcadolph/whotoask/internal/secret"
)

// assessDeparturesListed caps how many people the departure file covers.
const assessDeparturesListed = 10

// assessMinPlaceWork is the least credited work a directory needs before it
// is reported as a system.
const assessMinPlaceWork = 30

// assessPlacesListed caps how many systems the deliverable lists.
const assessPlacesListed = 40

// assessArgs holds the flags of the assess command.
type assessArgs struct {
	// RepoPaths are the local repository roots to read.
	RepoPaths []string
	// SlackExport is a Slack export zip or its unzipped folder.
	SlackExport string
	// OrgCSV is the org chart CSV.
	OrgCSV string
	// Codeowners is a CODEOWNERS file or a repo root holding one.
	Codeowners string
	// OutDir is the output directory, named by date when empty.
	OutDir string
	// AliasesFile is the alias file applied before the inferred joins.
	AliasesFile string
	// GithubRepos are the owner/name repositories whose reviews to read.
	GithubRepos []string
	// GithubURL is the API root for GitHub Enterprise Server.
	GithubURL string
	// GithubPages is how many hundreds of pull requests to read per repository.
	GithubPages int
	// Top is how many people the departure file covers.
	Top int
	// GitSinceDays is how far back to read git history.
	GitSinceDays int
	// MaxCommits is the commit cap per repository.
	MaxCommits int
}

// hasInput reports whether the arguments name at least one source to read.
func (a *assessArgs) hasInput() bool {
	return len(a.RepoPaths) > 0 || a.SlackExport != "" || a.OrgCSV != "" ||
		a.Codeowners != "" || len(a.GithubRepos) > 0
}

// assessSources is the index an assessment builds and the connectors whose
// side tallies the systems view reads after the fetch.
type assessSources struct {
	// Index holds every record the inputs yielded.
	Index *index.Index
	// Git is the git history connector, nil when no repository was named.
	Git *connector.GitHistory
	// GitHub is the review connector, nil when no GitHub repository was named.
	GitHub *connector.GitHub
}

// assessment is every result an assessment computes before it renders.
type assessment struct {
	// Findings are the scored subjects.
	Findings []resolve.TopicRisk
	// Regions are the bodies of work that change together under one lead.
	Regions []resolve.Region
	// Ownership compares declared owners with the observed work.
	Ownership resolve.OwnershipReport
	// Departures are what the record shows resting on each key person.
	Departures []resolve.DepartureImpact
	// Spans are the crossings between subjects only one person has made.
	Spans []resolve.Span
	// Places are the significant directories and who their work rests on.
	Places []resolve.Place
	// Checks are the git commands that recount each place.
	Checks []resolve.CheckCommand
	// Ramp is what moving into a new area has cost in this history.
	Ramp resolve.Ramp
	// Split are the display names still held by more than one record.
	Split []resolve.SplitName
	// Aliases are the joins the rules declined to make alone.
	Aliases []resolve.AliasSuggestion
	// Successors is who stands nearest to what rests on each key person.
	Successors []personSuccession
	// Systems reports whether git history was read, so the systems and checks
	// files are written even when no place clears the floor.
	Systems bool
}

// newAssessCmd builds the assess command, which turns a directory of exports
// into a sealed knowledge-risk assessment: the deliverable, not the app.
func newAssessCmd(opts *options) *cobra.Command {
	var a assessArgs
	cmd := &cobra.Command{
		Use:   "assess",
		Short: "Produce a sealed knowledge-risk assessment from local exports",
		Long: `Build a knowledge-risk assessment for one company from the material a data
room provides: local git clones, a Slack export zip, an org chart CSV, a
CODEOWNERS file. Nothing is fetched from the network and no credentials are
used, unless you also point it at a GitHub repository to read reviews from,
which is the one thing a data room rarely contains and the only way to see
the people who approve changes rather than write them. The index is built fresh in memory and never saved, so an assessment never
mixes with your own index.

The output directory holds the deliverable:

  summary.md           the executive summary: findings, questions, actions
  report.html          the knowledge-risk brief, readable without whotoask
  findings.json        every scored topic: bus factor, level, experts
  ownership.json       where the owner on paper is not the one doing the work
  systems.json         each significant directory and who its work rests on
  checks.json          a git command per system, so any row can be checked
  departures.json      what the record shows resting on each key person
  successors.json      who stands nearest to what rests on each key person
  ramp.json            what moving into a new area has cost in this history
  identity.json        which records were judged one person, and which were not
  aliases.json         the suggested joins, in the format --aliases reads
  manifest.json        every file above by size and sha256, signed
  manifest.sig         the signature; check it all with whotoask verify
  README.txt           what each file is and how to verify the seal

An identity left split reads as two people covering each other, so it flatters
every coverage figure. Run it once, review aliases.json, and run it again with
--aliases pointing at the reviewed file.

Examples:
  whotoask assess --repo-path ./target-repo --out acme-assessment
  whotoask assess --repo-path ./target-repo --github-repo acme/platform
  whotoask assess --repo-path ./svc-a --repo-path ./svc-b --slack-export export.zip
  whotoask assess --repo-path ./target-repo --aliases acme-assessment/aliases.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAssess(cmd, opts, &a)
		},
	}
	a.register(cmd)
	return cmd
}

// register binds the assess flags to the arguments.
func (a *assessArgs) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringArrayVar(&a.RepoPaths, "repo-path", nil, "Local repository root, repeatable.")
	f.StringVar(&a.SlackExport, "slack-export", "", "Slack export zip or its unzipped folder.")
	f.StringVar(&a.OrgCSV, "org-csv", "", "Org chart CSV.")
	f.StringVar(&a.Codeowners, "codeowners", "", "CODEOWNERS file or a repo root holding one.")
	f.StringArrayVar(&a.GithubRepos, "github-repo", nil,
		"Repository as owner/name whose reviews to read, repeatable. Needs "+
			githubTokenEnv+". Reviews are credited to the places they reviewed, "+
			"which is what finds the people who approve rather than commit.")
	f.StringVar(&a.GithubURL, "github-url", "",
		"API root, for GitHub Enterprise Server. Empty uses github.com.")
	f.IntVar(&a.GithubPages, "github-pages", 20,
		"Pages of pull requests to read per repository, in hundreds.")
	f.StringVar(&a.AliasesFile, "aliases", "",
		"Alias file joining identifiers that belong to one person, applied before "+
			"the inferred joins. The deliverable's aliases.json is written in this "+
			"format, so reviewing it and passing it back is the fix for a split "+
			"identity.")
	f.StringVar(&a.OutDir, "out", "", "Output directory (default whotoask-assessment-<date>).")
	f.IntVar(&a.Top, "top", assessDeparturesListed, "People to cover in the departure file.")
	f.IntVar(&a.GitSinceDays, "git-since-days", 730,
		"How far back to read git history. Two years, because an assessment is about "+
			"what the company knows, not what it did this quarter.")
	f.IntVar(&a.MaxCommits, "max-commits", 100000,
		"Commit cap per repository. Far above the indexing default: a cap that "+
			"truncates history silently drops the people who built the older half of "+
			"a system, which is exactly what an assessment is asked to find.")
}

// runAssess reads the inputs, scores them, writes the deliverable, and seals it.
func runAssess(cmd *cobra.Command, opts *options, a *assessArgs) error {
	if !a.hasInput() {
		return fmt.Errorf("%w: name at least one input: --repo-path, --slack-export, "+
			"--org-csv, or --codeowners", ErrBadArgs)
	}
	if a.OutDir == "" {
		a.OutDir = "whotoask-assessment-" + opts.now().Format("20060102")
	}
	if err := os.MkdirAll(a.OutDir, 0o755); err != nil {
		return fmt.Errorf("assess: %w", err)
	}
	log := cmd.ErrOrStderr()

	src, err := readAssessSources(cmd.Context(), opts, a, log)
	if err != nil {
		return err
	}
	ix := src.Index
	if err := joinAssessIndex(ix, a.AliasesFile); err != nil {
		return err
	}
	res := scoreAssessment(src, a, log)
	del, err := renderAssessment(ix, res, a.OutDir, opts.now())
	if err != nil {
		return err
	}
	if err := del.write(); err != nil {
		return err
	}

	// The seal is taken over the bytes that were just written, read back
	// from disk, so a verifier pointed at this directory checks the
	// reader's own copy rather than a copy held in memory.
	priv, err := opts.attestKey()
	if err != nil {
		return err
	}
	if _, err := sealFinding(a.OutDir, priv, ix, res.Findings, del.sealed, opts.now()); err != nil {
		return err
	}

	reportAssess(log, ix, res.Findings, res.Departures, res.Split, a.OutDir)
	return nil
}

// readAssessSources fetches every named input into a fresh in-memory index.
func readAssessSources(ctx context.Context, opts *options, a *assessArgs, log io.Writer) (assessSources, error) {
	src := assessSources{Index: index.New(index.WithNow(opts.clock))}
	ix := src.Index
	if a.OrgCSV != "" {
		oc := connector.NewOrgCSV(a.OrgCSV)
		oc.Log = log
		recs, err := oc.Fetch(ctx)
		if err != nil {
			return src, err
		}
		ix.Add(recs)
	}
	if len(a.RepoPaths) > 0 {
		src.Git = connector.NewGitHistory(connector.GitOptions{
			Paths: a.RepoPaths, SinceDays: a.GitSinceDays, MaxCommits: a.MaxCommits, Log: log,
		})
		recs, err := src.Git.Fetch(ctx)
		if err != nil {
			return src, err
		}
		ix.Add(recs)
	}
	if a.Codeowners != "" {
		recs, err := connector.NewCodeOwners(a.Codeowners).Fetch(ctx)
		if err != nil {
			return src, err
		}
		ix.Add(recs)
	}
	if len(a.GithubRepos) > 0 {
		token := secret.Resolve(githubTokenEnv)
		if token == "" {
			return src, fmt.Errorf("%w: --github-repo needs %s", ErrBadArgs, githubTokenEnv)
		}
		src.GitHub = connector.NewGitHub(token, connector.GitHubOptions{
			Repos: a.GithubRepos, MaxPages: a.GithubPages, BaseURL: a.GithubURL, Log: log,
		})
		recs, err := src.GitHub.Fetch(ctx)
		if err != nil {
			return src, explainSourceError("GitHub", githubTokenEnv, err)
		}
		ix.Add(recs)
	}
	if a.SlackExport != "" {
		se := connector.NewSlackExport(a.SlackExport, connector.SlackExportOptions{
			IncludePrivate: true, SinceDays: 36500, Log: log,
		})
		recs, err := se.Fetch(ctx)
		if err != nil {
			return src, err
		}
		ix.Add(recs)
	}
	return src, nil
}

// joinAssessIndex applies the alias file and the inferred joins, and refuses
// an index that holds nobody.
func joinAssessIndex(ix *index.Index, aliasesFile string) error {
	// Aliases before the inferred joins, so a stated identity is a
	// premise the rules reason from rather than a correction applied
	// after they have already decided. The report names the splits it
	// could not join and tells the reader to fix them here; until this
	// flag existed that advice could not be followed.
	if aliasesFile != "" {
		if err := ix.LoadAliases(aliasesFile); err != nil {
			return err
		}
	}
	ix.AutoJoin()
	ix.Canonicalize()
	if len(ix.Graph.People) == 0 {
		return fmt.Errorf("%w: the inputs yielded no people; check the paths", ErrBadArgs)
	}
	return nil
}

// scoreAssessment computes every finding the deliverable reports.
func scoreAssessment(src assessSources, a *assessArgs, log io.Writer) *assessment {
	ix := src.Index
	// Everything is scored once and passed down. Risk walks every person
	// and every subject and folds the compound names together, and the
	// departure view needs it per person: recomputing it there made the
	// cost of an assessment grow with the square of the organization.
	res := &assessment{
		Findings: resolve.Risk(ix, 0),
		Regions:  resolve.Regions(ix, 0),
	}
	res.Ownership = resolve.Ownership(ix)
	res.Departures = assessDepartures(ix, res.Findings, res.Regions, a.Top)
	res.Spans = resolve.SoleSpans(ix, 0)
	reviewers := assessPlaces(res, src, a, log)
	res.Split = resolve.SplitNames(ix)
	res.Aliases = resolve.SuggestAliases(ix, res.Split)
	res.Successors = assessSuccessors(ix, res.Departures, res.Places, reviewers)
	return res
}

// assessPlaces fills the systems view from the git connector's per-directory
// tally and returns who reviewed each directory. It does nothing without git.
func assessPlaces(res *assessment, src assessSources, a *assessArgs, log io.Writer) map[string]map[model.ID]bool {
	git, gh := src.Git, src.GitHub
	if git == nil {
		return nil
	}
	res.Systems = true
	var reviewers map[string]map[model.ID]bool
	// Systems are places, not words: the per-directory tally
	// answers who a system rests on without pooling every path
	// that shares its name.
	dirWork, totals := git.DirWork(), git.WorkTotals()
	if gh != nil {
		// A review is evidence of holding the place it reviewed.
		// The merges name their pull requests, so the link needs
		// no extra request, and without it a reviewer is credited
		// with the words of a title instead of the code.
		warnUnmatchedRepos(log, a.GithubRepos, git.RepoIDs())
		dirWork = resolve.AddReviewCredit(dirWork, totals,
			git.PullDirs(), gh.PullPeople())
		reviewers = reviewersByDir(src.Index, git.PullDirs(), gh.PullPeople())
	}
	res.Places = resolve.PlaceLeads(src.Index, dirWork, totals,
		assessMinPlaceWork, 3)
	if len(res.Places) > assessPlacesListed {
		res.Places = res.Places[:assessPlacesListed]
	}
	res.Checks = resolve.PlaceChecks(res.Places, git.PlaceLabels(), a.GitSinceDays)
	res.Ramp = resolve.MeasureRamp(git.Ramps(), res.Places)
	return reviewers
}

// renderAssessment renders every file of the deliverable, unwritten.
func renderAssessment(ix *index.Index, res *assessment, outDir string, now time.Time) (*deliverable, error) {
	del := newDeliverable(outDir)
	del.json("findings.json", "findings", res.Findings)
	del.json("ownership.json", "ownership", res.Ownership)
	del.json("departures.json", "departures", res.Departures)
	del.json("crossings.json", "crossings", res.Spans)
	del.json("joined-work.json", "joined work", res.Regions)
	del.json("identity.json", "identity", identityRecord{
		People: model.PeopleCount(ix.Graph.People), Merged: ix.Joins(), Split: res.Split,
		Suggested: res.Aliases,
	})
	if len(res.Aliases) > 0 {
		// Written in the format --aliases reads, so reviewing this file
		// and passing it back is the whole fix. A report that names a
		// problem and hands over the correction is doing the reader's
		// next step for them; one that only names it is a complaint.
		del.json("aliases.json", "aliases", resolve.AliasFile(res.Aliases))
	}
	if res.Systems {
		del.json("systems.json", "systems", res.Places)
		del.json("checks.json", "checks", res.Checks)
	}
	if len(res.Successors) > 0 {
		del.json("successors.json", "successors", res.Successors)
	}
	if res.Ramp.Arrivals > 0 {
		del.json("ramp.json", "ramp", res.Ramp)
	}
	del.text("summary.md", "summary", "text/markdown", assessSummary(ix, res))
	html, err := riskHTML(ix, res.Findings, res.Regions, res.Spans, briefFindings{
		Systems: res.Places, Checks: res.Checks, Ramp: res.Ramp, Split: res.Split,
		Aliases: res.Aliases, Successors: res.Successors, Merged: len(ix.Joins()),
	}, briefRows, now)
	if err != nil {
		return nil, err
	}
	del.file("report.html", "report", "text/html", html)
	del.text("README.txt", "readme", "text/plain", assessReadme(del.names()))
	return del, nil
}

// assessDepartures reports, for the people who lead the most subjects, what
// the record shows resting on them. It is a present finding about coverage
// rather than a prediction: a measured holdout showed this model does not
// forecast who will be active later, and the wording throughout says what is
// in the record rather than what will happen.
//
// The scored subjects and the joined regions are passed in because both are
// walks of the whole graph and every person's view is cut from the same two.
func assessDepartures(
	ix *index.Index, findings []resolve.TopicRisk, regions []resolve.Region, top int,
) []resolve.DepartureImpact {
	seen := make(map[string]bool)
	var out []resolve.DepartureImpact
	for _, tr := range findings {
		for _, e := range tr.Experts {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			imp := resolve.DepartureFrom(ix, findings, regions, e.ID)
			if len(imp.Sole)+len(imp.Top) > 0 {
				out = append(out, imp)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Sole) != len(out[j].Sole) {
			return len(out[i].Sole) > len(out[j].Sole)
		}
		if len(out[i].Top) != len(out[j].Top) {
			return len(out[i].Top) > len(out[j].Top)
		}
		return out[i].Person < out[j].Person
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

// identityRecord is every judgement an assessment made about who is who.
//
// It is the first thing to check when a finding looks wrong, because every
// number in the deliverable rests on it: one person counted twice halves the
// concentration in everything they touch, and two people counted as one
// invents a hole nobody has. Publishing the decisions beside the findings is
// what makes them arguable rather than asserted.
type identityRecord struct {
	// People is how many distinct people the index settled on.
	People int `json:"people"`
	// Merged are the records folded together, with the evidence for each.
	Merged []index.Join `json:"merged"`
	// Split are the display names still held by more than one record, which
	// read as extra cover the organization does not have.
	Split []resolve.SplitName `json:"split"`
	// Suggested are the joins the rules declined to make alone, with the
	// evidence, for a human to accept or reject.
	Suggested []resolve.AliasSuggestion `json:"suggested,omitempty"`
}

// personSuccession is one key person and who stands nearest to what rests on
// them.
type personSuccession struct {
	// Person is the holder's canonical id, and Name their display name.
	Person string `json:"person"`
	Name   string `json:"name"`
	// Dir is the place the succession was judged against, empty when the
	// holder leads no reported system and only subjects were compared.
	Dir string `json:"dir,omitempty"`
	// Nearest is the best-placed candidate. Absent when nobody clears the
	// evidence floor, which NobodyClose then says outright.
	Nearest *resolve.Successor `json:"nearest,omitempty"`
	// NobodyClose is set when the record offers no natural second, which is a
	// sharper finding than a candidate, not a gap in the report.
	NobodyClose bool `json:"nobodyClose,omitempty"`
}

// assessSuccessors names, for each person in the departure view, who stands
// nearest to what rests on them. A finding without a next move is a worry; a
// name with evidence beside it is something a reader can act on in the room.
func assessSuccessors(
	ix *index.Index, departures []resolve.DepartureImpact, places []resolve.Place,
	reviewers map[string]map[model.ID]bool,
) []personSuccession {
	// The place each person most singly holds, so place evidence attaches to
	// the directory the finding is about rather than an arbitrary one.
	topPlace := make(map[string]string)
	for _, pl := range places {
		if len(pl.Holders) == 0 || pl.Bus > 1 {
			continue
		}
		holder := pl.Holders[0].ID
		if _, taken := topPlace[holder]; !taken {
			topPlace[holder] = pl.Dir
		}
	}
	in := resolve.NewSuccessorInputs(ix, places, reviewers)
	out := make([]personSuccession, 0, len(departures))
	for _, d := range departures {
		entry := personSuccession{Person: d.Person, Name: d.Name, Dir: topPlace[d.Person]}
		near, ok := in.SuccessorFor(ix, model.ID(d.Person), entry.Dir)
		if ok {
			entry.Nearest = &near
		} else {
			entry.NobodyClose = true
		}
		out = append(out, entry)
	}
	return out
}

// reviewersByDir maps each directory to the canonical people who took part in
// pull requests that changed it. It is the succession model's third evidence
// source: a reviewer has read the area without writing it.
func reviewersByDir(
	ix *index.Index, pullDirs map[connector.PullRef][]string,
	pullPeople map[connector.PullRef][]string,
) map[string]map[model.ID]bool {
	out := make(map[string]map[model.ID]bool)
	for pull, dirs := range pullDirs {
		for _, login := range pullPeople[pull] {
			if login == "" {
				continue
			}
			id := ix.CanonicalID(model.ID("github:" + strings.ToLower(login)))
			for _, dir := range dirs {
				if out[dir] == nil {
					out[dir] = make(map[model.ID]bool)
				}
				out[dir][id] = true
			}
		}
	}
	return out
}

// warnUnmatchedRepos reports a named forge repository that none of the read
// clones claims to be.
//
// Review credit is joined by repository and number, so a clone whose remote
// names a different repository, or names none at all, credits nobody from the
// reviews fetched for it. That failure is silent and looks exactly like a
// project where reviewers happen not to hold anything, which is the opposite
// of what the flag was passed to find. One clone with no remote still works,
// because a number belonging to a single repository is unambiguous; naming
// two repositories and reading two anonymous clones does not.
func warnUnmatchedRepos(log io.Writer, named, resolved []string) {
	if len(named) == 0 {
		return
	}
	have := make(map[string]bool, len(resolved))
	anonymous := 0
	for _, id := range resolved {
		if id == "" {
			anonymous++
			continue
		}
		have[strings.ToLower(id)] = true
	}
	var missing []string
	for _, name := range named {
		if !have[strings.ToLower(strings.TrimSpace(name))] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	if anonymous > 0 && len(missing) == 1 && len(named) == 1 {
		// One repository named and a clone that cannot say what it is: the
		// number identifies the pull request on its own, so the join still
		// holds and there is nothing to report.
		return
	}
	fmt.Fprintf(log,
		"assess: no clone claims to be %s, so reviews fetched for it credit nobody. "+
			"Check the repository name and that the clone has the matching remote\n",
		strings.Join(missing, ", "))
}

// reportAssess writes the closing lines of a run: what was measured, and
// anything about the inputs that should change how the numbers are read.
func reportAssess(
	log io.Writer, ix *index.Index, findings []resolve.TopicRisk,
	departures []resolve.DepartureImpact, split []resolve.SplitName, outDir string,
) {
	critical, sole := 0, 0
	for _, f := range findings {
		if f.Level == "critical" {
			critical++
		}
		if len(f.Experts) == 1 {
			sole++
		}
	}
	fmt.Fprintf(log,
		"assess: %d %s across %v; %d subjects scored, %d critical, "+
			"%d with one person in the record; departure view for %d %s\n",
		model.PeopleCount(ix.Graph.People), people(model.PeopleCount(ix.Graph.People)), ix.SourceNames(),
		len(findings), critical, sole, len(departures), people(len(departures)))
	if len(split) > 0 {
		names := make([]string, 0, len(split))
		for i, s := range split {
			if i == 3 {
				break
			}
			names = append(names, fmt.Sprintf("%s (%d)", s.Name, len(s.IDs)))
		}
		fmt.Fprintf(log,
			"assess: %d names are held by more than one record and were not joined: %s. "+
				"Each unjoined split reads as extra cover, so coverage here is an "+
				"upper bound. Fix with an alias file or a .mailmap and run again\n",
			len(split), strings.Join(names, ", "))
	}
	fmt.Fprintf(log, "assess: wrote %s\n", outDir)
	fmt.Fprintf(log, "assess: %s\n", notForLine)
}

// assessFile is one file of the deliverable, held until the whole set renders.
type assessFile struct {
	// Name is the file name inside the output directory.
	Name string
	// MediaType is the file's media type.
	MediaType string
	// Data is the exact bytes to write, which the seal digests.
	Data []byte
}

// deliverable gathers what an assessment writes so the files are rendered
// first, written together, and sealed over the bytes that shipped. Sealing a
// re-encoding of a file instead produces a valid bundle whose evidence matches
// nothing the reader received, which reads as a proof and is not one.
type deliverable struct {
	// dir is the output directory.
	dir string
	// files are the deliverable's files, in the order they were added.
	files []assessFile
	// sealed mirrors files as the entries of the seal's manifest.
	sealed []seal.File
	// err holds the first rendering failure, reported by write.
	err error
}

// newDeliverable starts an empty deliverable for one output directory.
func newDeliverable(dir string) *deliverable { return &deliverable{dir: dir} }

// file adds one rendered file under the role it plays in the finding.
func (d *deliverable) file(name, role, mediaType string, data []byte) {
	d.files = append(d.files, assessFile{Name: name, MediaType: mediaType, Data: data})
	d.sealed = append(d.sealed, seal.File{Path: name, MediaType: mediaType, Role: role})
}

// text adds one rendered text file.
func (d *deliverable) text(name, role, mediaType, body string) {
	d.file(name, role, mediaType, []byte(body))
}

// json adds one file as indented JSON.
func (d *deliverable) json(name, role string, v any) {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		if d.err == nil {
			d.err = fmt.Errorf("assess: %s: %w", name, err)
		}
		return
	}
	d.file(name, role, "application/json", append(data, '\n'))
}

// names lists the files the deliverable holds, in the order they were added.
func (d *deliverable) names() []string {
	out := make([]string, 0, len(d.files))
	for _, f := range d.files {
		out = append(out, f.Name)
	}
	return out
}

// write puts every file on disk. Every file is rendered before any is written,
// so a template or encoding failure leaves nothing behind at all. A write that
// fails partway does leave the files before it, which is why the seal is
// written last: a directory without one is visibly unfinished.
func (d *deliverable) write() error {
	if d.err != nil {
		return d.err
	}
	for _, f := range d.files {
		if err := os.WriteFile(filepath.Join(d.dir, f.Name), f.Data, 0o644); err != nil {
			return fmt.Errorf("assess: %w", err)
		}
	}
	return nil
}

// assessFileNotes explains each file of the deliverable to somebody who has
// never run whotoask. The README lists only the files this run produced, so it
// never describes a file that is not there.
var assessFileNotes = map[string]string{
	"summary.md": "The executive summary: what was found, the questions it\n" +
		"                     raises for management, and the actions it suggests.",
	"report.html": "The knowledge-risk brief. Open it in a browser.",
	"findings.json": "Every scored subject: bus factor, risk level, and the\n" +
		"                     people who hold it.",
	"ownership.json": "Areas whose declared owner is not the person doing the\n" +
		"                     work in them.",
	"systems.json": "Each significant directory of the code base and the\n" +
		"                     people its observed work rests on, breadth-discounted so\n" +
		"                     wide-ranging committers do not outrank owners.",
	"departures.json": "For each of the most load-bearing people, the subjects\n" +
		"                     the record shows resting on them: sole means nobody else\n" +
		"                     appears in the record for it at all.",
	"crossings.json": "Pairs of subjects only one person has ever worked\n" +
		"                     across. Both may have plenty of experts; the link\n" +
		"                     between them has one.",
	"joined-work.json": "Bodies of subjects that change together where the same\n" +
		"                     person leads every one, so they would be picked up as a\n" +
		"                     whole rather than a subject at a time.",
	"identity.json": "Which records were judged to be the same person and why,\n" +
		"                     and which same-named records were left apart. Check this\n" +
		"                     first if a finding looks wrong: every number rests on it.",
	"successors.json": "For each key person, who stands nearest to what rests on\n" +
		"                     them, with the evidence: works there already, has\n" +
		"                     reviewed it, shares the subjects. Nearest by the record,\n" +
		"                     not a judgement of ability.",
	"checks.json": "One paste-ready git command per reported system, counting\n" +
		"                     the same history the finding was counted from, so any\n" +
		"                     row can be checked without whotoask.",
	"ramp.json": "What this history says about moving into an unfamiliar\n" +
		"                     area: how long the people who did it took, and how many\n" +
		"                     started and did not carry on. Observed, not predicted.",
	"aliases.json": "The suggested joins in the format --aliases reads.\n" +
		"                     Review it, drop what is wrong, and re-run with\n" +
		"                     --aliases to fix the split identities in one pass.",
	"README.txt": "This file.",
}

// assessReadme explains the deliverable, naming only the files it holds. The
// README describes itself and the seal files, which are written after the list
// is taken and so are never in it.
func assessReadme(names []string) string {
	var b strings.Builder
	b.WriteString("This directory is a whotoask knowledge-risk assessment.\n\n")
	for _, name := range names {
		note := assessFileNotes[name]
		if note == "" {
			continue
		}
		fmt.Fprintf(&b, "%-20s %s\n", name, note)
	}
	fmt.Fprintf(&b, "%-20s %s\n", "README.txt", assessFileNotes["README.txt"])
	fmt.Fprintf(&b, "%-20s %s\n", seal.ManifestName,
		"Every file above by its size and sha256.")
	fmt.Fprintf(&b, "%-20s %s\n", seal.SignatureName,
		"An ed25519 signature over the exact bytes of manifest.json.")
	b.WriteString(`
Checking the seal takes one command, run from inside this directory, and it
sends nothing anywhere:

    whotoask verify .

It checks that manifest.sig is a valid signature over manifest.json, then that
every file above still has the size and sha256 the manifest records. It names
any file that was altered and any file that is missing, and exits non-zero if
there is either.

The signature proves the manifest has not changed since it was signed, by the
key it names. To know whose key that is, ask whoever sent this directory for
the output of whotoask attest key, and add --key with that public key.
`)
	return b.String()
}
