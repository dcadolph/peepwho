package ownersbench

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/index"
)

// newSurvivalRepo builds two areas that both lose their leading person after
// the cutoff. The single-held one has nobody behind it and falls silent; the
// covered one has others who carry on. That contrast is the whole claim.
func newSurvivalRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	files := make(map[string]string)
	var pending []string
	var stream bytes.Buffer
	write := func(rel, content string) {
		files[rel] = content
		pending = append(pending, rel)
	}
	commit := func(who []string, msg string) {
		t.Helper()
		importCommit(t, &stream, who, msg, files, pending)
		pending = nil
	}
	// Before the cutoff: one person alone in "lonely", three in "covered".
	for i := range 14 {
		write(fmt.Sprintf("lonely/f%02d.go", i), fmt.Sprintf("%d\n", i))
		commit(relAuthor("Only One", "only@x.com", 700-i*5), "lonely work")
	}
	for i := range 24 {
		who := []string{"Ann Cover", "Bo Cover", "Cy Cover"}[i%3]
		mail := []string{"ann@x.com", "bo@x.com", "cy@x.com"}[i%3]
		write(fmt.Sprintf("covered/f%02d.go", i), fmt.Sprintf("%d\n", i))
		commit(relAuthor(who, mail, 700-i*5), "covered work")
	}
	// After the cutoff: the leader of each area is gone. Nobody replaces the
	// lonely one; the covered one keeps moving.
	for i := range 18 {
		who := []string{"Bo Cover", "Cy Cover"}[i%2]
		mail := []string{"bo@x.com", "cy@x.com"}[i%2]
		write(fmt.Sprintf("covered/later%02d.go", i), fmt.Sprintf("%d\n", i))
		commit(relAuthor(who, mail, 300-i*15), "covered continues")
	}
	runGitIn(t, dir, &stream, "fast-import", "--quiet")
	runGit(t, dir, "reset", "-q", "--hard")
	return dir
}

// TestRunSurvivalSeparatesFragility verifies the forward claim the product
// makes: a place the record shows resting on one person falls silent when
// that person stops, while a covered place survives losing its leader.
func TestRunSurvivalSeparatesFragility(t *testing.T) {
	// Deliberately not parallel: this fixture builds a hundred-commit
	// repository with real git, and four of those running at once under the
	// race detector is enough concurrent git to make CI flaky. They finish in
	// seconds serially.
	dir := newSurvivalRepo(t)
	git := connector.NewGitHistory(connector.GitOptions{
		Paths: []string{dir}, SinceDays: 1095, UntilDays: 365,
	})
	recs, err := git.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	ix := index.New()
	ix.Build(recs)
	ix.AutoJoin()
	ix.Canonicalize()

	res, err := RunSurvival(ix, SurvivalConfig{
		Repo: dir, SinceDays: 1095, CutoffDays: 365, MinPast: 5,
		DirWork: git.DirWork(), WorkTotals: git.WorkTotals(),
	})
	if err != nil {
		t.Fatalf("RunSurvival: %v", err)
	}
	byDir := make(map[string]SurvivalDir)
	for _, d := range res.Dirs {
		byDir[d.Dir] = d
	}
	lonely, ok := byDir["lonely"]
	if !ok {
		t.Fatalf("lonely not judged; dirs = %+v", res.Dirs)
	}
	if !lonely.Concentrated {
		t.Errorf("lonely = %+v, want it called single-held", lonely)
	}
	if lonely.HolderStayed {
		t.Errorf("lonely holder is recorded as staying; the fixture retired them")
	}
	if !lonely.WentQuiet {
		t.Errorf("lonely = %+v, want it to have gone quiet", lonely)
	}
	covered, ok := byDir["covered"]
	if !ok {
		t.Fatalf("covered not judged")
	}
	if covered.Concentrated {
		t.Errorf("covered = %+v, want it not called single-held", covered)
	}
	if covered.WentQuiet {
		t.Errorf("covered = %+v, want it to have carried on without its leader", covered)
	}

	fq, f, oq, o := res.Rates()
	if f == 0 {
		t.Fatal("no flagged place lost its holder; the fixture proves nothing")
	}
	if fq != f {
		t.Errorf("flagged quiet %d of %d, want all of them", fq, f)
	}
	if o > 0 && oq == o {
		t.Errorf("every covered place went quiet too, so the split says nothing")
	}

	// The benchmark has to judge the predicate the product flags on, not one
	// that resembles it. It once used a raw head count of everyone who had
	// ever touched a directory, so a place where one person holds most of the
	// work and two others touched a file each was scored in the control
	// column, and the published rate described a flag nobody is shown.
	for _, d := range res.Dirs {
		place, ok := dirPlace(ix, Config{
			DirWork: git.DirWork(), WorkTotals: git.WorkTotals(), TopK: 3,
		}, d.Dir)
		if !ok {
			t.Errorf("%s was judged but has no place tally", d.Dir)
			continue
		}
		if d.Concentrated != (place.Bus <= 1) {
			t.Errorf("%s called single-held=%v but the place the product shows has "+
				"bus %d over %d people", d.Dir, d.Concentrated, place.Bus, place.People)
		}
		if d.Holder != place.Holders[0].Name {
			t.Errorf("%s holder = %q, want the place's own top holder %q",
				d.Dir, d.Holder, place.Holders[0].Name)
		}
	}
}
