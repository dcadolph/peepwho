package cmd

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/resolve"
	"github.com/dcadolph/whotoask/internal/simorg"
)

// demoPerson is who the demo's recall view starts as: the engineer the sample
// conversations involve.
const demoPerson = "angela@example.com"

// demoQuery is the question the demo opens with, so the first thing a new
// user sees is an answered page.
const demoQuery = "who do I talk to about billing retries"

const (
	// demoMaxCommits bounds a --repo run so a large repository still starts in
	// seconds. It is the reason the demo is a demo and not an assessment.
	demoMaxCommits = 4000
	// demoSinceDays is how far back a --repo run reads.
	demoSinceDays = 730
	// demoMinPlaceWork is the least credited work a directory needs before the
	// demo will make a claim about who holds it.
	demoMinPlaceWork = 20
	// demoPlacesListed caps the places the demo shows.
	demoPlacesListed = 40
)

// newDemoCmd builds the demo command, which explores whotoask on a simulated
// company with no credentials or setup.
func newDemoCmd(opts *options) *cobra.Command {
	var cfg webConfig
	var big bool
	var saveIndex string
	var embed bool
	var repoPaths []string
	maxCommits := demoMaxCommits
	open := true
	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Run whotoask on your own repository, or on a simulated company",
		Long: `Serve the web UI over an index built here and now.

Point it at a repository you already have and it reads that repository's git
history and opens on what it found: the directories the work rests on, and the
ones resting on a single person. Every name is checkable against the same git
log, which is the point of running it on your own code rather than on a sample.

  whotoask demo --repo .

With no --repo it builds a simulated company instead. The simulation covers
all eight sources, so identity joins, recency, confidence, and feedback all
behave as they would on real data, but nobody can check it against anything.

Either way nothing is fetched from the network and no credentials are needed.
Nothing is written outside a temporary directory, discarded when the demo stops.

To try cloud AI on the sample data, export a provider key (such as
WHOTOASK_ANTHROPIC_KEY) and add --policy redacted. Only do this for a demo you keep
to yourself: the demo serves with no token, so a publicly reachable one with a key
set becomes an open relay to your paid account.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg.memoryKey = true
			if len(repoPaths) > 0 {
				return demoRepo(cmd, opts, cfg, repoPaths, maxCommits, open)
			}

			dir, err := os.MkdirTemp("", "whotoask-demo-*")
			if err != nil {
				return fmt.Errorf("demo: %w", err)
			}
			defer func() { _ = os.RemoveAll(dir) }()

			buildIndex, buildEpisodes := simorg.BuildIndex, simorg.BuildEpisodes
			recallMe, query, sources := demoPerson, demoQuery, 8
			if big {
				buildIndex, buildEpisodes = simorg.BuildBigIndex, simorg.BuildBigEpisodes
				recallMe, query = simorg.BigDemoPerson()
				sources = 8
			}

			fmt.Fprintln(cmd.ErrOrStderr(), "whotoask demo: indexing a simulated company (sample data only)")
			ix, err := buildIndex(dir)
			if err != nil {
				return err
			}
			store, err := feedback.Load(dir+"/feedback.json", nil)
			if err != nil {
				return err
			}
			if cfg.episodes, err = buildEpisodes(ix); err != nil {
				return err
			}
			if saveIndex != "" {
				return saveDemo(cmd, ix, cfg.episodes, saveIndex, embed, cfg)
			}
			// The demo is public sample data, so it serves open with no token
			// and, having no real user, starts recall as the person the sample
			// conversations were had with.
			cfg.public = true
			cfg.recallMe = recallMe

			link := "http://" + cfg.addr + "/?q=" + url.QueryEscape(query)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"whotoask demo: %d people, %d channels across %d sources\nwhotoask demo: try %s\n",
				len(ix.Graph.People), len(ix.Graph.Channels), sources, link)
			if open {
				go openBrowser(link)
			}

			return serveWeb(cmd, opts, engine.New(ix, opts.pol), store, cfg)
		},
	}
	addWebFlags(cmd, &cfg, "127.0.0.1:8765")
	cmd.Flags().StringArrayVar(&repoPaths, "repo", nil,
		"Run the demo on a real repository instead of the simulation, repeatable. "+
			"Nothing leaves the machine.")
	cmd.Flags().IntVar(&maxCommits, "max-commits", demoMaxCommits,
		"Commits to read per repository with --repo.")
	cmd.Flags().BoolVar(&big, "big", false,
		"Serve a large simulated company of 200+ people instead of the small sample")
	cmd.Flags().BoolVar(&open, "open", true, "Open the demo in a browser on start")
	cmd.Flags().StringVar(&saveIndex, "save-index", "",
		"Write the simulated company to this directory as a real index, then exit")
	cmd.Flags().BoolVar(&embed, "embed", false,
		"Generate embeddings while saving, so the saved index answers in semantic mode. Needs Ollama.")
	return cmd
}

// demoRepo serves the demo over a real repository rather than the simulation.
//
// The simulation can show every source, but nobody can check it: a visitor
// asked to believe a finding about a company that does not exist has been
// given a screenshot, not evidence. A repository they already have on disk
// names people they can look up in its own git log, which is the only version
// of this demo that survives a skeptical reader.
//
// It reads git only, so recall stays off: git records what changed, not what
// was said, and offering an empty conversation view would imply the data was
// there and disappointing rather than absent by construction.
func demoRepo(
	cmd *cobra.Command, opts *options, cfg webConfig, paths []string, maxCommits int, open bool,
) error {
	log := cmd.ErrOrStderr()
	fmt.Fprintf(log, "whotoask demo: reading %s\n", strings.Join(paths, ", "))

	git := connector.NewGitHistory(connector.GitOptions{
		Paths: paths, SinceDays: demoSinceDays, MaxCommits: maxCommits, Log: log,
	})
	recs, err := git.Fetch(cmd.Context())
	if err != nil {
		return err
	}
	ix := index.New()
	ix.Add(recs)
	ix.AutoJoin()
	ix.Canonicalize()
	if len(ix.Graph.People) == 0 {
		return fmt.Errorf("%w: %s yielded no people; is it a git repository with history?",
			ErrBadArgs, strings.Join(paths, ", "))
	}

	// Every place is scored, then the exposed ones are kept. Cutting by size
	// first would throw away the findings before they were ranked.
	cfg.places = resolve.MostFragile(
		resolve.PlaceLeads(ix, git.DirWork(), git.WorkTotals(), demoMinPlaceWork, 3),
		demoPlacesListed)

	dir, err := os.MkdirTemp("", "whotoask-demo-*")
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	store, err := feedback.Load(dir+"/feedback.json", nil)
	if err != nil {
		return err
	}

	// Real data about real people is not sample data, so this half of the
	// demo keeps the token and stays on loopback: the exemption that lets the
	// simulation serve open exists because there is nothing there to protect.
	link := "http://" + cfg.addr + "/#/exposure"
	people, places := len(ix.Graph.People), len(cfg.places)
	fmt.Fprintf(log, "whotoask demo: %d %s, %d place%s, %d resting on one person\n",
		people, personNoun(people), places, plural(places), soleHeld(cfg.places))
	fmt.Fprintf(log, "whotoask demo: open %s\n", link)
	if open {
		go openBrowser(link)
	}
	return serveWeb(cmd, opts, engine.New(ix, opts.pol), store, cfg)
}

// personNoun picks the noun for a head count, so a one-person repository does
// not report "1 people".
func personNoun(n int) string {
	if n == 1 {
		return "person"
	}
	return "people"
}

// soleHeld counts the places whose work rests on one person, which is the
// number the demo leads with. It reads the bus factor rather than the length
// of the holder list, which is capped and would count every place as sole-held
// on a repository with a cap of one.
func soleHeld(places []resolve.Place) int {
	n := 0
	for _, p := range places {
		if p.Bus <= 1 {
			n++
		}
	}
	return n
}

// saveDemo writes the simulated company to dir as a real index, so the same
// sample data the web demo serves can be driven from the command line: ask,
// risk, ownership, related, and attest all read an index from a data directory.
// Embedding is opt-in because it needs a local model and takes a while.
func saveDemo(
	cmd *cobra.Command, ix *index.Index, eps *episode.Store, dir string, embed bool, cfg webConfig,
) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	if embed {
		fmt.Fprintln(cmd.ErrOrStderr(), "whotoask demo: embedding, this takes a minute")
		if err := ix.Embed(cmd.Context(), engine.NewDocOllama(cfg.embedModel, cfg.ollamaURL)); err != nil {
			return fmt.Errorf("demo: embed: %w", err)
		}
	}
	if err := ix.Save(filepath.Join(dir, "index.json")); err != nil {
		return fmt.Errorf("demo: save index: %w", err)
	}
	if eps != nil {
		if err := eps.Save(filepath.Join(dir, "episodes.json")); err != nil {
			return fmt.Errorf("demo: save episodes: %w", err)
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"whotoask demo: wrote the sample company to %s\nwhotoask demo: try whotoask --data-dir %s risk\n",
		dir, dir)
	return nil
}

// openBrowser makes a best-effort attempt to open link in the default
// browser once the server has had a moment to come up.
func openBrowser(link string) {
	time.Sleep(300 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", link)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", link)
	default:
		cmd = exec.Command("xdg-open", link)
	}
	_ = cmd.Run()
}
