package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/policy"
	"github.com/dcadolph/peepwho/internal/vault"
)

// TestGuardArchive covers the gate on keeping the words of a conversation. It
// is the only thing standing between a run and full conversation text on disk.
func TestGuardArchive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Indexer func(t *testing.T) *Indexer
		Want    error
	}{{ // Test 0: The organization's policy forbids retaining content.
		Indexer: func(t *testing.T) *Indexer {
			return &Indexer{Dir: Dir(t.TempDir()), Policy: policy.New(policy.Open, false).WithoutArchive()}
		},
		Want: ErrBadArgs,
	}, { // Test 1: Allowed by policy, but no key is set to encrypt it.
		Indexer: func(t *testing.T) *Indexer {
			return &Indexer{Dir: Dir(t.TempDir()), Policy: policy.New(policy.Open, false)}
		},
		Want: ErrBadArgs,
	}, { // Test 2: Allowed by policy and encrypted, with no license anywhere.
		Indexer: func(t *testing.T) *Indexer {
			c := testKeyCodec(t)
			return &Indexer{
				Dir: Dir(t.TempDir()), Policy: policy.New(policy.Open, false),
				Codec: func() (vault.Codec, error) { return c, nil },
			}
		},
		Want: nil,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			x := test.Indexer(t)
			x.Log = &bytes.Buffer{}
			if err := x.guardArchive(); !errors.Is(err, test.Want) {
				t.Errorf("guardArchive = %v, want %v", err, test.Want)
			}
		})
	}
}

// TestUpdateWatermarkConcurrent verifies that concurrent index runs against
// different sources do not drop each other's cursor. The state file is a shared
// read-modify-write, so without the file lock two runs race and one source's
// watermark is lost.
func TestUpdateWatermarkConcurrent(t *testing.T) {
	t.Parallel()
	x := &Indexer{Dir: Dir(t.TempDir())}
	sources := []string{"jira", "confluence", "slack", "github"}
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func(i int, src string) {
			defer wg.Done()
			rec := connector.Record{Source: src, Time: base.Add(time.Duration(i) * time.Hour)}
			if err := x.updateWatermark(src, "", false, []connector.Record{rec}); err != nil {
				t.Errorf("updateWatermark(%s): %v", src, err)
			}
		}(i, src)
	}
	wg.Wait()

	st, err := NewStore(x.Dir, nil).LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for i, src := range sources {
		wm, ok := st.Get(src, "")
		if !ok {
			t.Errorf("source %q cursor lost to a concurrent write", src)
			continue
		}
		if want := base.Add(time.Duration(i) * time.Hour); !wm.Cursor.Equal(want) {
			t.Errorf("source %q cursor = %v, want %v", src, wm.Cursor, want)
		}
	}
}

// TestIndexerUnlock verifies a merge into a sealed index asks Unlock for the
// key, keeps asking until a candidate opens the index, and saves under the key
// that worked. With no Unlock the sealed error stands and nothing is written.
func TestIndexerUnlock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		WantLabels  []string
		WantRecords []string
		Want        error
		NoUnlock    bool
	}{{ // Test 0: A wrong key, then the right one.
		WantLabels:  []string{IndexPassphrase},
		WantRecords: []string{"org-csv file:"},
		Want:        nil,
	}, { // Test 1: No Unlock leaves the sealed error and records nothing.
		NoUnlock: true,
		Want:     vault.ErrEncrypted,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			csv := filepath.Join(dir, "people.csv")
			content := "name,email,title,team,topics\n" +
				"Jane Doe,jane@x.com,Engineer,Billing,retries\n" +
				"Raj Patel,raj@x.com,Engineer,Billing,invoices\n"
			if err := os.WriteFile(csv, []byte(content), 0o600); err != nil {
				t.Fatalf("write csv: %v", err)
			}
			key := testKeyCodec(t)
			sealed := &Indexer{Dir: Dir(dir), Codec: func() (vault.Codec, error) { return key, nil }}
			req := IndexRequest{Source: "org-csv", IndexArgs: IndexArgs{File: csv}}
			if err := sealed.Index(context.Background(), req); err != nil {
				t.Fatalf("seal: %v", err)
			}
			before, err := os.ReadFile(sealed.Dir.Index())
			if err != nil {
				t.Fatalf("read index: %v", err)
			}

			var labels, records []string
			x := &Indexer{Dir: Dir(dir), Log: &bytes.Buffer{}}
			x.Record = func(source, scope string) error {
				records = append(records, source+" "+scope[:len("file:")])
				return nil
			}
			if !test.NoUnlock {
				x.Unlock = func(label string, try func(vault.Codec) error) (vault.Codec, error) {
					labels = append(labels, label)
					if err := try(vault.NewPassphraseCipher([]byte("wrong"))); err == nil {
						t.Error("a wrong passphrase opened the index")
					}
					if err := try(key); err != nil {
						return nil, err
					}
					return key, nil
				}
			}
			req.Merge = true
			err = x.Index(context.Background(), req)
			if !errors.Is(err, test.Want) {
				t.Fatalf("Index = %v, want %v", err, test.Want)
			}
			if diff := cmp.Diff(test.WantLabels, labels); diff != "" {
				t.Errorf("unlock labels mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(test.WantRecords, records); diff != "" {
				t.Errorf("records mismatch (-want +got):\n%s", diff)
			}
			after, err := os.ReadFile(x.Dir.Index())
			if err != nil {
				t.Fatalf("read index after: %v", err)
			}
			if test.Want != nil && !bytes.Equal(before, after) {
				t.Error("a failed unlock rewrote the index")
			}
			if _, err := NewStore(x.Dir, nil).LoadIndex(); !errors.Is(err, vault.ErrEncrypted) {
				t.Errorf("plain load after the run = %v, want the index still sealed", err)
			}
			if _, err := NewStore(x.Dir, key).LoadIndex(); err != nil {
				t.Errorf("keyed load after the run: %v", err)
			}
		})
	}
}

// testKeyCodec returns an index cipher with a fixed test key.
func testKeyCodec(t *testing.T) vault.Codec {
	t.Helper()
	c, err := vault.NewKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}
