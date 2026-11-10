package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/llm"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/policy"
	"github.com/dcadolph/whotoask/internal/util"
	"github.com/dcadolph/whotoask/internal/vault"
)

// Passphrase labels name the sealed file an Unlock is asked to open, so a
// caller that prompts can say which one it wants.
const (
	// IndexPassphrase labels the index.
	IndexPassphrase = "Index passphrase"
	// EpisodesPassphrase labels the conversation store.
	EpisodesPassphrase = "Conversations passphrase"
)

// UnlockFunc obtains a codec for a sealed file the configured codec could not
// open. It calls try with each candidate and returns the first one try
// accepts. A terminal prompts for a passphrase; an app reads its keychain.
type UnlockFunc func(label string, try func(vault.Codec) error) (vault.Codec, error)

// Indexer reads sources and folds them into the index in a data directory. It
// never prompts: credentials, keys, and the refresh record come through its
// function fields, so the command line and an app drive it the same way.
type Indexer struct {
	// Dir is the data directory the index and its sidecars live in.
	Dir Dir
	// Codec resolves the at-rest codec. It is called where a sealed file is
	// first read or written, never earlier, so a bad key surfaces after the
	// fetch as it always has. Nil means plain JSON.
	Codec func() (vault.Codec, error)
	// Unlock is called when a file is sealed and Codec cannot open it. Nil
	// leaves the sealed error as the result.
	Unlock UnlockFunc
	// Policy is the egress policy for private channels, archives, and the
	// embedding host.
	Policy policy.Policy
	// PolicyFor resolves the policy a refreshed scope runs under from the
	// --policy value it was recorded with, empty when none was. Nil runs every
	// scope under Policy.
	PolicyFor func(recorded string) (policy.Policy, error)
	// Creds reads one credential by its environment variable name.
	Creds func(env string) string
	// Record saves the flags a source was indexed with under its refresh
	// scope. Nil records nothing, which is what a refresh replaying a record
	// wants.
	Record func(source, scope string) error
	// Outcome receives what a successful index read, once, after the fold is
	// saved. Nil reports nothing.
	Outcome func(Outcome)
	// Log receives progress, warnings, and the change report.
	Log io.Writer
	// Stdin is what a source reading standard input reads.
	Stdin io.Reader
	// Now is the clock the index and the watermarks read. Nil means the system
	// clock.
	Now func() time.Time
	// unlocked is the codec an Unlock returned. It serves every later read and
	// write, so one passphrase covers the rest of the run.
	unlocked vault.Codec
}

// Outcome is what one successful index read, for a caller that reports it in
// plain words rather than as a refresh scope key. The refresh config keeps it
// per scope as what that scope last found.
type Outcome struct {
	// Repos is how many repositories the git source read, zero for other sources.
	Repos int `json:"repos"`
	// People is how many people in the index hold a record from this source, counted
	// after identities are joined, so it never exceeds the headline.
	People int `json:"people"`
	// Channels is how many distinct channels the source holds after the run.
	Channels int `json:"channels"`
}

// heldRecords returns what ix holds after a fold for source and for every source
// recs name. A full read holds exactly what it fetched; an incremental read
// fetches only what changed, often nothing, while the index still holds the rest,
// so counting the fetch would report an unchanged source as finding no one.
func heldRecords(ix *index.Index, source string, recs []connector.Record) []connector.Record {
	names := map[string]bool{source: true}
	for _, r := range recs {
		names[r.Source] = true
	}
	var held []connector.Record
	for _, name := range slices.Sorted(maps.Keys(names)) {
		held = append(held, ix.SourceRecords(name)...)
	}
	return held
}

// countRecords returns how many people in ix hold a person record among recs, and
// the distinct channels among recs by name. A record names its person by source
// id, else by email, as the index keys it, and the name resolves through the
// joined identities, so one person seen under many addresses or accounts counts
// once and the count never exceeds the people ix holds. A record with neither id
// nor email is not counted.
func countRecords(ix *index.Index, recs []connector.Record) (people, channels int) {
	persons := map[model.ID]bool{}
	chans := map[string]bool{}
	for _, r := range recs {
		switch r.Kind {
		case connector.KindPerson:
			key := strings.ToLower(strings.TrimSpace(r.PersonID))
			if key == "" {
				key = util.NormalizeEmail(r.Email)
			}
			if key == "" {
				continue
			}
			if id := ix.CanonicalID(model.ID(key)); ix.Graph.People[id] != nil {
				persons[id] = true
			}
		case connector.KindChannel:
			if r.Name != "" {
				chans[r.Name] = true
			}
		}
	}
	return len(persons), len(chans)
}

// IndexRequest names one source and the flags to read it with.
type IndexRequest struct {
	// Source is the registry name of the source to read.
	Source string
	// IndexArgs holds every other index flag.
	IndexArgs
}

// FoldParams holds the index-build knobs shared by the index and connect
// commands: how records fold into the existing graph and how the result
// decays, embeds, and reports.
type FoldParams struct {
	// Merge adds records onto the existing index instead of replacing it.
	Merge bool
	// Incremental folds a since-limited fetch into the source's existing
	// records instead of replacing them, so a partial re-read keeps the people
	// and topics it did not re-fetch. It always runs inside a merge.
	Incremental bool
	// AllowShrink accepts a source that returned far less than it did last
	// time, which is otherwise refused as a truncated read.
	AllowShrink bool
	// HalfLifeDays halves a dated record's weight after this many days; 0
	// disables decay.
	HalfLifeDays int
	// AliasesFile joins one person across sources by canonical id when set.
	AliasesFile string
	// Embed generates embeddings via Ollama when true.
	Embed bool
	// EmbedModel names the Ollama embedding model; empty uses the default.
	EmbedModel string
	// OllamaURL is the Ollama base URL for embedding.
	OllamaURL string
	// ChangesFile writes the index diff as JSON to this path when set.
	ChangesFile string
	// Episodes are the conversations a source observed, stored beside the
	// index so recall can point back at them.
	Episodes []episode.Episode
}

// Index reads one source and folds it into the index: it decides whether the
// run is incremental, builds the source from the registry, indexes the
// records, and saves the refresh record and the watermark the next run resumes
// from.
func (x *Indexer) Index(ctx context.Context, req IndexRequest) error {
	if req.Archive {
		if err := x.guardArchive(); err != nil {
			return err
		}
	}
	scope := IndexScope(req.Source, ScopeInputs{
		JiraJQL: req.JiraJQL, JiraProjects: req.JiraProjects,
		ConfluenceCQL: req.ConfluenceCQL, ConfluenceSpaces: req.ConfluenceSpaces,
		GitHubRepos: req.Repos, GitHubOrg: req.GitHubOrg, SlackPrivate: req.IncludePrivate,
	})
	// An explicit --jira-jql or --confluence-cql is authoritative, so the
	// connector ignores the watermark and returns the full result. Folding
	// that would stack a copy every run, so such a source is never treated
	// as incremental: it replaces on every merge, as a full read does.
	canInc := incrementalCapable(req.Source) && !UsesExplicitQuery(req.Source, req.JiraJQL, req.ConfluenceCQL)
	since, incremental, err := x.watermark(&req, canInc, scope)
	if err != nil {
		return err
	}
	// Git keeps a position per repository rather than one time for the
	// whole source, so its incremental state is decided separately.
	// Getting this wrong is not a slow read but a wrong index: an
	// increment that is not folded REPLACES everything git contributed
	// with just the newest commits, and the shrink guard is the only
	// thing that catches it.
	var gitStops map[string]string
	if req.Source == "git" {
		gitStops = x.gitMarks(req.Merge && !req.Full, req.RepoPaths)
		incremental = len(gitStops) > 0
	}
	spec, ok := SourceByName(req.Source)
	if !ok {
		return fmt.Errorf("%w: %q (want %s)", ErrUnknownSource, req.Source, SourceNames())
	}
	o := req.sourceOptions(x, since, gitStops)
	o.Creds = x.resolveCreds(spec)
	recs, eps, src, err := FetchSource(ctx, spec, o)
	// Git resumes from a commit rather than a time, so it carries its own
	// marks: where this run stopped, saved below.
	var gitAfter map[string]string
	var out Outcome
	if g, isGit := src.(*connector.GitHistory); isGit {
		gitAfter = g.Marks()
		out.Repos = g.Repos()
	}
	if err != nil {
		return err
	}
	// A connector may hand back what it read before a cancel with no error. Folding
	// that would store a partial read as complete.
	if err := ctx.Err(); err != nil {
		return err
	}
	ix, err := x.fold(ctx, recs, FoldParams{
		Merge: req.Merge, Incremental: incremental, AllowShrink: req.AllowShrink, HalfLifeDays: req.HalfLifeDays,
		AliasesFile: req.AliasesFile, Embed: req.Embed, EmbedModel: req.EmbedModel, OllamaURL: req.OllamaURL,
		ChangesFile: req.ChangesFile, Episodes: eps,
	})
	if err != nil {
		return err
	}
	out.People, out.Channels = countRecords(ix, heldRecords(ix, req.Source, recs))
	if err := x.saveProgress(&req, scope, canInc, gitAfter, recs); err != nil {
		return err
	}
	if x.Outcome != nil {
		x.Outcome(out)
	}
	return nil
}

// saveProgress runs after a successful index. It records the flags this
// source was indexed with, so `whotoask refresh` and a scheduled refresh can
// replay them, then saves the position the next incremental run resumes from.
// The refresh record is keyed by scope, so indexing repo A and then repo B in
// separate runs keeps both; keyed by source alone, the second run silently
// replaced the first and the weekly refresh let A go stale forever. Saving it
// is best-effort: a failure warns but does not fail the index.
func (x *Indexer) saveProgress(
	req *IndexRequest, scope string, canInc bool, gitAfter map[string]string, recs []connector.Record,
) error {
	if x.Record != nil {
		rscope := RefreshScope(req.Source, scope, req.File, req.RepoPaths)
		if err := x.Record(req.Source, rscope); err != nil {
			fmt.Fprintf(x.log(), "warning: could not save refresh config: %v\n", err)
		}
	}
	// Git's position is a commit, not a time, so it saves its own.
	if len(gitAfter) > 0 {
		if err := x.saveGitMarks(gitAfter); err != nil {
			fmt.Fprintf(x.log(), "warning: could not save the git position: %v\n", err)
		}
	}
	// Advance the incremental watermark only after a successful index, so a
	// failed run never moves it.
	if canInc && req.Source != "git" {
		return x.updateWatermark(req.Source, scope, req.Full, recs)
	}
	return nil
}

// sourceOptions turns the flags into the registry's plain source options. The
// credentials are resolved separately, for the chosen source only.
func (r *IndexRequest) sourceOptions(x *Indexer, since time.Time, gitStops map[string]string) SourceOptions {
	episodes := r.Episodes || r.Archive
	return SourceOptions{
		File:                 r.File,
		Stdin:                x.Stdin,
		Log:                  x.log(),
		Since:                since,
		AllowPrivateChannels: x.Policy.AllowPrivateChannels(),
		Slack: connector.SlackOptions{
			IncludePrivate: r.IncludePrivate, JoinPublic: r.SlackJoin,
			SinceDays: r.SinceDays, MaxMessages: r.MaxMessages,
			Episodes: episodes, MaxEpisodesPerChannel: r.MaxEpisodes,
			Archive: r.Archive, MaxArchiveMessages: r.MaxArchive,
		},
		SlackExport: connector.SlackExportOptions{
			IncludePrivate: r.IncludePrivate, SinceDays: r.SinceDays, MaxMessages: r.MaxMessages,
			Episodes: episodes, MaxEpisodesPerChannel: r.MaxEpisodes,
			Archive: r.Archive, MaxArchiveMessages: r.MaxArchive,
		},
		GitHub: connector.GitHubOptions{
			Repos: r.Repos, Org: r.GitHubOrg, MaxRepos: r.MaxRepos,
			ResolveEmails: r.GitHubEmails, Episodes: episodes,
		},
		JiraURL: r.JiraURL,
		Jira: connector.JiraOptions{
			Projects: r.JiraProjects, JQL: r.JiraJQL, MaxIssues: r.MaxIssues,
			Episodes: episodes, Server: r.JiraServer,
		},
		ConfluenceURL: r.ConfluenceURL,
		Confluence: connector.ConfluenceOptions{
			Spaces: r.ConfluenceSpaces, CQL: r.ConfluenceCQL, MaxPages: r.MaxPages,
			Server: r.ConfluenceServer,
		},
		PagerDuty: connector.PagerDutyOptions{Episodes: episodes},
		Git: connector.GitOptions{
			Paths: r.RepoPaths, SinceDays: r.GitSinceDays, UntilDays: r.GitUntilDays,
			MaxCommits: r.MaxCommits, Workers: r.GitWorkers, StopAt: gitStops,
		},
	}
}

// resolveCreds reads the credentials the source names, and only those, so
// indexing one source never touches another's keychain entry.
func (x *Indexer) resolveCreds(spec SourceSpec) map[string]string {
	creds := make(map[string]string, len(spec.Creds))
	for _, env := range spec.Creds {
		if x.Creds != nil {
			creds[env] = x.Creds(env)
		} else {
			creds[env] = ""
		}
	}
	return creds
}

// Fold folds recs into the on-disk index and reports what changed. It loads
// the previous graph for the diff, builds or merges, auto-joins handle
// identities, canonicalizes, optionally embeds, then saves atomically. The
// index and connect commands share it so ingest lives in one place.
func (x *Indexer) Fold(ctx context.Context, recs []connector.Record, p FoldParams) error {
	_, err := x.fold(ctx, recs, p)
	return err
}

// fold is Fold returning the index it saved.
func (x *Indexer) fold(ctx context.Context, recs []connector.Record, p FoldParams) (*index.Index, error) {
	// The whole load-merge-save is one read-modify-write across three files:
	// the index, its sources sidecar, and the episodes beside them. Two runs
	// interleaving here, a Sunday 3am scheduled refresh over a manual --merge,
	// would each save over the other's records while both report success, so
	// the same cross-process lock the state file already takes guards it.
	unlock, err := util.LockFile(x.Dir.Index() + util.LockSuffix)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Load any existing index once. A missing file is a first run; any other
	// error, including an encrypted index with no key, aborts so re-indexing
	// never overwrites an index it could not read.
	existing, err := x.loadIndex()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	var prev *model.Graph
	if existing != nil {
		prev = existing.Graph
	}

	ix, err := x.foldBase(existing, p)
	if err != nil {
		return nil, err
	}
	if err := foldRecords(ix, existing, recs, p); err != nil {
		return nil, err
	}
	x.joinIdentities(ix)
	if p.Embed {
		if err := x.embed(ctx, ix, p); err != nil {
			return nil, err
		}
	}

	changes := index.Diff(prev, ix.Graph)
	// Read the change log before anything is written, so a log the codec
	// cannot open fails the fold instead of leaving a saved index behind an
	// error.
	history, err := x.loadChanges()
	if err != nil {
		return nil, err
	}
	if err := x.saveIndex(ix); err != nil {
		return nil, err
	}
	if err := x.saveEpisodes(ctx, ix, p); err != nil {
		return nil, err
	}
	// A first build diffs against nothing, so everyone would read as joined.
	// That is no change anyone looks for, so only a fold over an index logs.
	if prev != nil && !changes.Empty() {
		if err := x.saveChanges(append(history, ChangeEntry{At: x.now().UTC(), Changes: changes})); err != nil {
			return nil, err
		}
	}
	x.reportFold(ix, changes)
	if p.ChangesFile != "" {
		if err := writeChangesFile(p.ChangesFile, changes); err != nil {
			return nil, err
		}
	}
	return ix, nil
}

// foldBase returns the index a fold writes into: the existing one with every
// source's records restored for a merge, or a fresh one, with the half-life
// and alias file applied.
func (x *Indexer) foldBase(existing *index.Index, p FoldParams) (*index.Index, error) {
	ix := index.New(index.WithNow(x.Now))
	if p.Merge && existing != nil {
		// A merge rebuilds from every source, so the records for the sources
		// already indexed must come back from the sidecar first; without them
		// the rebuild would keep only the source being added.
		if err := x.loadSources(existing); err != nil {
			return nil, fmt.Errorf(
				"cannot merge: %w; run a full index once without --merge to rebuild the sources file", err)
		}
		ix = existing
	}
	ix.SetHalfLife(time.Duration(p.HalfLifeDays) * 24 * time.Hour)
	if p.AliasesFile != "" {
		if err := ix.LoadAliases(p.AliasesFile); err != nil {
			return nil, err
		}
	}
	return ix, nil
}

// foldRecords adds recs to ix as an incremental merge, a merge, or a full
// replace, refusing a replace that would shrink or empty a good index.
func foldRecords(ix, existing *index.Index, recs []connector.Record, p FoldParams) error {
	// An incremental read returns only what changed, so it is legitimately far
	// smaller than the last full read; the shrink guard, which protects a full
	// replace from a truncated read, does not apply to it.
	if !p.Incremental {
		if err := GuardShrink(existing, recs, p.AllowShrink); err != nil {
			return err
		}
	}
	switch {
	case p.Incremental && existing != nil:
		ix.MergeIncremental(recs)
	case p.Merge:
		ix.Add(recs)
	default:
		// A replacing run that read nothing would write an empty index over a
		// good one and report success. The usual cause is a token that expired
		// or lost a scope, which each connector reports as a skip rather than
		// as a failure, so the existing index is kept instead.
		if len(recs) == 0 {
			return fmt.Errorf(
				"%w: the existing index was left alone. Check the messages above: "+
					"the usual cause is a token that expired or lost a scope. "+
					"Use --merge to add to an index without replacing it", ErrNoRecords)
		}
		ix.Build(recs)
	}
	return nil
}

// joinIdentities auto-joins handle identities, reports the joins and the
// ambiguous handles, and canonicalizes the graph.
func (x *Indexer) joinIdentities(ix *index.Index) {
	res := ix.AutoJoin()
	if res.Joined > 0 {
		fmt.Fprintf(x.log(), "auto-joined %d handle identities\n", res.Joined)
	}
	if len(res.Ambiguous) > 0 {
		fmt.Fprintf(x.log(),
			"%d handle(s) share a name with more than one person and were left unresolved; add them to the alias file: %s\n",
			len(res.Ambiguous), strings.Join(res.Ambiguous, ", "))
	}
	ix.Canonicalize()
}

// embed computes vectors for every person and channel through Ollama, behind
// the egress policy guard.
func (x *Indexer) embed(ctx context.Context, ix *index.Index, p FoldParams) error {
	if err := GuardLLMHost(x.Policy, p.OllamaURL); err != nil {
		return err
	}
	total := len(ix.Graph.People) + len(ix.Graph.Channels)
	fmt.Fprintf(x.log(),
		"embedding %d people and %d channels via Ollama...\n",
		len(ix.Graph.People), len(ix.Graph.Channels))
	ix.SetEmbedProgress(util.ProgressWriter(x.log(), "embedded", embedProgressEvery(total)))
	if err := ix.Embed(ctx, NewDocOllama(p.EmbedModel, p.OllamaURL)); err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	return nil
}

// reportFold writes what the saved index holds, warns when it is plaintext,
// and lists the changes since the last run.
func (x *Indexer) reportFold(ix *index.Index, changes index.Changes) {
	out := x.log()
	// Subjects and words are counted apart on purpose. A subject is something a
	// source stated: a label, a component, a directory. A word is something
	// mined from prose so a question phrased in somebody's own vocabulary still
	// matches. On a real issue tracker the mined words outnumber the stated
	// subjects four hundred to one, and reporting the total as "topics" made the
	// index look like it understood four hundred times more than it does.
	stated := 0
	for _, t := range ix.Graph.Topics {
		if t.Curated {
			stated++
		}
	}
	fmt.Fprintf(out,
		"indexed %d people, %d channels, %d teams, %d subjects and %d words from text into %s\n",
		len(ix.Graph.People), len(ix.Graph.Channels), len(ix.Graph.Teams),
		stated, len(ix.Graph.Topics)-stated, x.Dir.Index())
	if c, _ := x.codec(); c == nil && len(ix.Graph.People) > 0 {
		fmt.Fprintln(out,
			"note: this index holds names, emails, and titles in plaintext, protected only by file "+
				"permissions. Set WHOTOASK_INDEX_KEY or WHOTOASK_INDEX_PASSPHRASE to encrypt it at rest.")
	}
	reportChanges(out, changes)
}

// guardArchive refuses to retain conversation content unless the
// organization's own policy allows it and an encryption key is set. Both
// checks are local: the policy is a file the organization controls, and the
// key never leaves this machine.
func (x *Indexer) guardArchive() error {
	if !x.Policy.AllowArchive() {
		return fmt.Errorf("%w: keeping conversation content is disabled by policy", ErrBadArgs)
	}
	// An archive is the only store that holds readable conversation text, so it
	// must be encrypted at rest. Refuse to write it in plaintext rather than
	// leave full conversations on disk behind a file mode alone.
	codec, err := x.codec()
	if err != nil {
		return err
	}
	if codec == nil {
		return fmt.Errorf(
			"%w: an archive retains the full text of conversations, so it must be encrypted at rest. "+
				"Set WHOTOASK_INDEX_KEY (base64 of 32 random bytes) or WHOTOASK_INDEX_PASSPHRASE and retry, "+
				"or drop --archive to keep only links", ErrBadArgs)
	}
	fmt.Fprintln(x.log(), "archive: keeping the words of each conversation, encrypted at rest")
	return nil
}

// saveEpisodes merges newly observed conversations into the episode store.
// Episodes always merge, even when the index itself is rebuilt: a source stops
// serving old messages long before they stop being worth remembering, so
// dropping them would throw away history whotoask may be the last to hold.
func (x *Indexer) saveEpisodes(ctx context.Context, ix *index.Index, p FoldParams) error {
	eps := p.Episodes
	if len(eps) == 0 {
		return nil
	}
	// Participants arrive as each source names them. Resolving them against
	// the graph is what makes one person's work findable across every tool.
	ix.CanonicalizeEpisodes(eps)
	// Note authors arrive source-native too, and only participants are
	// canonicalized above. Left as they came, a purge matching authors against
	// canonical identifiers misses them, so the two are resolved by the same
	// rule.
	for i := range eps {
		for j, n := range eps[i].Archive {
			if n.Author != "" {
				eps[i].Archive[j].Author = ix.CanonicalID(n.Author)
			}
		}
	}
	store, err := x.loadEpisodes()
	if err != nil {
		return err
	}
	// A conversation can only be embedded while its text is in hand: the store
	// keeps terms, not messages, so an episode indexed without embeddings can
	// never gain them later.
	var embedder *llm.Ollama
	if p.Embed {
		embedder = NewDocOllama(p.EmbedModel, p.OllamaURL)
		fmt.Fprintf(x.log(), "embedding %d conversations via Ollama...\n", len(eps))
	}
	before := store.Len()
	embedFailed := false
	for _, ep := range eps {
		// Include any retained archive text so semantic recall can match the
		// solution's meaning, not just the problem statement in the body. Keyword
		// postings already index the archive; the vector must too or an
		// archive is searchable by meaning only for the title and opener.
		body := strings.TrimSpace(ep.Body + " " + ep.Text() + " " + ep.ArchiveText())
		store.Add(ep)
		if embedder == nil || body == "" || embedFailed {
			continue
		}
		vec, err := embedder.Embed(ctx, body)
		if err != nil {
			// A failed embedding must not discard the conversations already
			// fetched: the index is on disk by now, so throwing these away
			// would leave the two out of step and waste the whole read. The
			// conversations are kept without vectors, so keyword recall works
			// and a later re-index can add the vectors.
			fmt.Fprintf(x.log(),
				"embedding stopped (%v); keeping conversations without semantic vectors, "+
					"re-index with a working model to add them\n", err)
			embedFailed = true
			continue
		}
		store.SetVector(ep.ID, vec)
	}
	st, err := x.store()
	if err != nil {
		return err
	}
	if err := st.SaveEpisodes(store); err != nil {
		return err
	}
	fmt.Fprintf(x.log(),
		"recorded %d conversations (%d new) into %s\n",
		len(eps), store.Len()-before, x.Dir.Episodes())
	return nil
}

// codec returns the codec an Unlock produced, else the configured one.
func (x *Indexer) codec() (vault.Codec, error) {
	if x.unlocked != nil {
		return x.unlocked, nil
	}
	if x.Codec == nil {
		return nil, nil
	}
	return x.Codec()
}

// store returns the store over the data directory under the current codec.
func (x *Indexer) store() (*Store, error) {
	c, err := x.codec()
	if err != nil {
		return nil, err
	}
	return NewStore(x.Dir, c).WithNow(x.Now), nil
}

// loadIndex reads the index, calling Unlock when it is sealed under a key the
// store does not have.
func (x *Indexer) loadIndex() (*index.Index, error) {
	st, err := x.store()
	if err != nil {
		return nil, err
	}
	ix, err := st.LoadIndex()
	if !errors.Is(err, vault.ErrEncrypted) || x.Unlock == nil {
		return ix, err
	}
	c, err := x.Unlock(IndexPassphrase, func(c vault.Codec) error {
		var lerr error
		ix, lerr = st.WithCodec(c).LoadIndex()
		return lerr
	})
	if err != nil {
		return nil, err
	}
	x.unlocked = c
	return ix, nil
}

// loadSources reads the sources sidecar into ix with the same key as the
// index.
func (x *Indexer) loadSources(ix *index.Index) error {
	st, err := x.store()
	if err != nil {
		return err
	}
	return st.LoadSources(ix)
}

// saveIndex writes the index under the current codec.
func (x *Indexer) saveIndex(ix *index.Index) error {
	st, err := x.store()
	if err != nil {
		return err
	}
	return st.SaveIndex(ix)
}

// loadChanges reads the change log under the current codec.
func (x *Indexer) loadChanges() ([]ChangeEntry, error) {
	st, err := x.store()
	if err != nil {
		return nil, err
	}
	return st.LoadChanges()
}

// saveChanges writes the change log under the current codec.
func (x *Indexer) saveChanges(entries []ChangeEntry) error {
	st, err := x.store()
	if err != nil {
		return err
	}
	return st.SaveChanges(entries)
}

// loadEpisodes reads the episode store, calling Unlock when it is sealed under
// a key the store does not have.
func (x *Indexer) loadEpisodes() (*episode.Store, error) {
	st, err := x.store()
	if err != nil {
		return nil, err
	}
	store, err := st.LoadEpisodes()
	if !errors.Is(err, vault.ErrEncrypted) || x.Unlock == nil {
		return store, err
	}
	c, err := x.Unlock(EpisodesPassphrase, func(c vault.Codec) error {
		var lerr error
		store, lerr = st.WithCodec(c).LoadEpisodes()
		return lerr
	})
	if err != nil {
		return nil, err
	}
	x.unlocked = c
	return store, nil
}

// indexExists reports whether an on-disk index is already present.
func (x *Indexer) indexExists() bool {
	_, err := os.Stat(x.Dir.Index())
	return err == nil
}

// log returns the progress writer, discarding when none is set.
func (x *Indexer) log() io.Writer {
	if x.Log == nil {
		return io.Discard
	}
	return x.Log
}

// now returns the current time on the indexer's clock.
func (x *Indexer) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

// embedProgressEvery picks how often to report embedding progress: about
// twenty updates across the whole run, and at least every entity for a small
// graph, so the line moves without flooding.
func embedProgressEvery(total int) int {
	if total <= 20 {
		return 1
	}
	return total / 20
}

// GuardShrink refuses to replace a source's contribution with a far smaller
// one. A rate limit or a scope a token quietly lost makes a connector keep
// what it managed to read and return no error, so without this a run that saw
// a fraction of a source would shrink the index while reporting success.
func GuardShrink(existing *index.Index, recs []connector.Record, allow bool) error {
	if existing == nil || allow {
		return nil
	}
	incoming := make(map[string]int)
	for _, rec := range recs {
		incoming[rec.Source]++
	}
	for name, got := range incoming {
		if name == "" {
			continue
		}
		had := existing.SourceSize(name)
		if had > 0 && got*2 < had {
			return fmt.Errorf(
				"%w: %s returned %d records where it last returned %d, so the existing index "+
					"was left alone. Check the messages above for a rate limit or a token that "+
					"lost a scope. Pass --allow-shrink if the source really did get smaller",
				ErrShrunkSource, name, got, had)
		}
	}
	return nil
}
