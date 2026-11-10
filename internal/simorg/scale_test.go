package simorg

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/resolve"
)

// scaleEnv opts a run into the scale measurements. They synthesize large
// companies and take minutes, so an ordinary `go test ./...` skips them.
const scaleEnv = "WHOTOASK_SCALE"

// embedDim is the vector width of a typical local embedding model, used to
// profile the memory and query cost of embeddings without standing up a model.
const embedDim = 768

// TestScale measures what whotoask costs as a company grows: how long ingest
// takes, how large the files get, how long a cold start takes, and how fast it
// answers, both by keyword and, with vectors filled, by meaning. The store is
// one JSON file read whole on every command, so the question this answers is
// where that stops being reasonable.
//
//	WHOTOASK_SCALE=1 go test ./internal/simorg/ -run TestScale -v -timeout 30m
func TestScale(t *testing.T) {
	if os.Getenv(scaleEnv) == "" {
		t.Skipf("set %s=1 to measure how whotoask scales", scaleEnv)
	}
	sizes := []struct {
		Name string
		Spec Spec
	}{
		{"team", Spec{People: 50, Channels: 20, Topics: 16, ThreadsPerChannel: 25, ChatterPerChannel: 200}},
		{"department", Spec{People: 250, Channels: 60, Topics: 16, ThreadsPerChannel: 60, ChatterPerChannel: 400}},
		{"company", Spec{People: 1000, Channels: 150, Topics: 16, ThreadsPerChannel: 100, ChatterPerChannel: 600}},
		{"enterprise", Spec{People: 5000, Channels: 400, Topics: 16, ThreadsPerChannel: 120, ChatterPerChannel: 800}},
		{"huge", Spec{People: 10000, Channels: 1000, Topics: 16, ThreadsPerChannel: 150, ChatterPerChannel: 800}},
	}
	t.Logf("%-11s %7s %7s %7s %8s %9s %9s %8s %8s %8s %8s %8s %9s %8s %8s",
		"size", "people", "convos", "terms", "ingest", "index", "estore", "save", "cold", "ask",
		"recall", "embed", "vectors", "sem", "heap")
	for _, size := range sizes {
		size.Spec.Seed = 11
		t.Run(size.Name, func(t *testing.T) {
			m := measure(t, size.Spec)
			t.Logf("%-11s %7d %7d %7d %8s %9s %9s %8s %8s %8s %8s %8s %9s %8s %9s",
				size.Name, m.people, m.episodes, m.postings, short(m.ingest), bytes(m.indexBytes),
				bytes(m.episodeBytes), short(m.save), short(m.coldStart), short(m.askLatency),
				short(m.recallLatency), short(m.embed), bytes(m.vectorBytes), short(m.semantic),
				bytes(int64(m.heap)))
		})
	}
}

// measurement is what one company size cost.
type measurement struct {
	// people and episodes are the size actually produced.
	people, episodes int
	// postings is the distinct-term vocabulary of the person index.
	postings int
	// ingest is how long the connectors and indexer took.
	ingest time.Duration
	// indexBytes and episodeBytes are the files on disk.
	indexBytes, episodeBytes int64
	// coldStart is how long loading both files from disk took, which every
	// command pays before it can answer anything.
	coldStart time.Duration
	// save is how long writing the index back took. It is measured separately
	// from the file size because it is not bounded by either: the whole
	// posting index is re-encoded on every write, so a refresh that read three
	// commits still pays this in full. It is the cost that grows worst with
	// size, and the one an incremental refresh does not avoid.
	save time.Duration
	// askLatency and recallLatency are median-ish answer times.
	askLatency, recallLatency time.Duration
	// embed is how long filling every person's vector took.
	embed time.Duration
	// vectorBytes is the index file's size once embeddings are stored, which
	// dwarfs the keyword index and is what semantic search costs on disk.
	vectorBytes int64
	// semantic is a median semantic answer time, the cost of the cosine scan
	// over every person that grows linearly with the company.
	semantic time.Duration
	// heap is bytes resident after loading, which is what a long-lived serve
	// or bot process holds.
	heap uint64
}

// measure builds a company of the given size and times the whole path.
func measure(t *testing.T, spec Spec) measurement {
	t.Helper()
	dir := t.TempDir()

	start := time.Now()
	built, err := Build(spec, dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	m := measurement{
		people:   len(built.Index.Graph.People),
		episodes: built.Episodes.Len(),
		ingest:   time.Since(start),
	}

	indexPath := filepath.Join(dir, "index.json")
	episodePath := filepath.Join(dir, "episodes.json")
	start = time.Now()
	if err := built.Index.Save(indexPath); err != nil {
		t.Fatalf("save index: %v", err)
	}
	m.save = time.Since(start)
	if err := built.Episodes.Save(episodePath); err != nil {
		t.Fatalf("save episodes: %v", err)
	}
	m.indexBytes = fileSize(t, indexPath)
	m.episodeBytes = fileSize(t, episodePath)

	// Cold start is what a person waits for before every single CLI answer.
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	start = time.Now()
	loadedIndex, err := index.Load(indexPath)
	if err != nil {
		t.Fatalf("load index: %v", err)
	}
	loadedEpisodes, err := episode.Load(episodePath)
	if err != nil {
		t.Fatalf("load episodes: %v", err)
	}
	m.coldStart = time.Since(start)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc {
		m.heap = after.HeapAlloc - before.HeapAlloc
	}

	m.postings = loadedIndex.PostingCount()
	m.askLatency = timeAsk(t, loadedIndex, built)
	m.recallLatency = timeRecall(t, loadedIndex, loadedEpisodes, built)
	m.embed, m.vectorBytes, m.semantic = measureVectors(t, dir, loadedIndex)
	return m
}

// measureVectors fills fake embeddings and measures what semantic search costs:
// the time to embed, the on-disk size of the vectors, and a median cosine-scan
// query. The values are meaningless for ranking; only the shape and count drive
// the cost, which is what a scale profile needs.
func measureVectors(t *testing.T, dir string, ix *index.Index) (time.Duration, int64, time.Duration) {
	t.Helper()
	start := time.Now()
	if err := ix.Embed(context.Background(), fakeEmbedder{}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	embedDur := time.Since(start)

	vecPath := filepath.Join(dir, "vectors.json")
	if err := ix.Save(vecPath); err != nil {
		t.Fatalf("save vectors: %v", err)
	}
	vecBytes := fileSize(t, vecPath)

	q, _ := fakeEmbedder{}.Embed(context.Background(), "who owns billing retries")
	var total time.Duration
	const runs = 15
	for range runs {
		s := time.Now()
		_ = ix.SemanticPeople(q, 5)
		total += time.Since(s)
	}
	return embedDur, vecBytes, per(total, runs)
}

// fakeEmbedder returns a deterministic unit vector per text, so a scale run can
// measure the cost of the cosine scan and the memory of the vectors without a
// real model.
type fakeEmbedder struct{}

// Embed returns a deterministic embedDim-wide unit vector seeded by text.
func (fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	rng := rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // Not security sensitive.
	v := make([]float32, embedDim)
	var norm float64
	for i := range v {
		v[i] = float32(rng.NormFloat64())
		norm += float64(v[i]) * float64(v[i])
	}
	if norm == 0 {
		return v, nil
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] /= float32(norm)
	}
	return v, nil
}

// timeAsk measures answering who-knows questions against a loaded index.
func timeAsk(t *testing.T, ix *index.Index, built *Built) time.Duration {
	t.Helper()
	res := resolve.NewKeyword(ix)
	var total time.Duration
	asked := 0
	for _, q := range built.Org.Questions {
		if q.Kind != KindWhoKnows {
			continue
		}
		start := time.Now()
		if _, err := res.Resolve(context.Background(), q.Text, 5); err != nil {
			t.Fatalf("ask: %v", err)
		}
		total += time.Since(start)
		asked++
	}
	return per(total, asked)
}

// timeRecall measures answering recall questions against loaded files.
func timeRecall(t *testing.T, ix *index.Index, store *episode.Store, built *Built) time.Duration {
	t.Helper()
	res := recall.New(store, ix)
	var total time.Duration
	asked := 0
	for _, q := range built.Org.Questions {
		if q.Kind != KindRecall || asked >= 200 {
			continue
		}
		start := time.Now()
		res.Resolve(context.Background(), recall.Query{Text: q.Text, Person: q.Asker, Limit: 5})
		total += time.Since(start)
		asked++
	}
	return per(total, asked)
}

// per averages a total over a count.
func per(total time.Duration, n int) time.Duration {
	if n == 0 {
		return 0
	}
	return total / time.Duration(n)
}

// fileSize returns a file's size on disk.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

// bytes renders a byte count the way a person reads one.
func bytes(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n > 1<<20:
		return fmt.Sprintf("%.0fMB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// short renders a duration without spurious precision.
func short(d time.Duration) string {
	switch {
	case d > time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d > time.Millisecond:
		return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	}
}
