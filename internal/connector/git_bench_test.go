package connector

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// BenchmarkGitFetch measures the heaviest thing peepwho does: reading a real
// repository's history. It needs a clone, so it only runs when one is named:
//
//	PEEPWHO_BENCH_REPO=/path/to/clone go test ./internal/connector/ \
//	  -run xxx -bench GitFetch -benchtime 1x -cpuprofile /tmp/cpu.prof
//
// PEEPWHO_BENCH_COMMITS caps the window; the default is enough to be
// representative without waiting minutes for every run.
//
// WHAT THIS MEASURED, on two years of home-assistant, so the next person does
// not repeat it. 8,000 commits take about 40 seconds and allocate 155 GB across
// 3.3 billion allocations, which is 413,000 allocations per commit. A profile
// puts 96% of that in go-git decoding trees inside its own diff walk:
// Tree.Decode 53%, transformChildren 18%, reached through merkletrie's compare.
//
// FIVE THINGS HAVE BEEN TRIED AND NONE OF THEM MOVED IT. A larger object cache
// and tree memoization were tried in an earlier round. Giving each worker a
// contiguous range instead of every Nth commit made it slightly WORSE (42.2s
// against 40.0s). Caching decoded root trees per worker, which was expected to
// pay once the ranges were contiguous, hit 49% of the time and still bought
// only 2%. Raising GOGC did nothing at 400 and cost 15% at 800.
//
// The reason they all fail is the same: the root tree decode is not the cost.
// The cost is the subtrees decoded inside merkletrie while it walks, and nothing
// outside that walk can reach them. Beating it means replacing the diff with one
// that caches subtrees by hash across commits, which is real work on
// correctness-critical code, since wrong changed paths quietly corrupt every
// measurement downstream.
//
// BEFORE BELIEVING ANY IMPROVEMENT HERE: run the unchanged code three times
// first. Variance is about 5% run to run, which is wider than four of the five
// results above.
func BenchmarkGitFetch(b *testing.B) {
	repo := os.Getenv("PEEPWHO_BENCH_REPO")
	if repo == "" {
		b.Skip("set PEEPWHO_BENCH_REPO to a git clone")
	}
	max := 8000
	if v := os.Getenv("PEEPWHO_BENCH_COMMITS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			max = n
		}
	}
	opts := GitOptions{Paths: []string{repo}, SinceDays: 730, MaxCommits: max}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		recs, err := NewGitHistory(opts).Fetch(context.Background())
		if err != nil {
			b.Fatalf("Fetch: %v", err)
		}
		if len(recs) == 0 {
			b.Fatal("no records")
		}
	}
}
