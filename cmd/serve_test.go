package cmd

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dcadolph/whotoask/internal/engine"
	"github.com/dcadolph/whotoask/internal/episode"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/policy"
	"github.com/dcadolph/whotoask/internal/seal"
)

// TestLoopbackAddr verifies which listen addresses count as loopback-only.
func TestLoopbackAddr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		In   string
		Want bool
	}{
		{In: "127.0.0.1:8765", Want: true},  // Test 0: IPv4 loopback.
		{In: "localhost:8765", Want: true},  // Test 1: Localhost by name.
		{In: "[::1]:8765", Want: true},      // Test 2: IPv6 loopback.
		{In: "0.0.0.0:8765", Want: false},   // Test 3: Every interface.
		{In: ":8765", Want: false},          // Test 4: Empty host binds everything.
		{In: "10.0.0.5:8765", Want: false},  // Test 5: A LAN address.
		{In: "host.corp:8765", Want: false}, // Test 6: A non-loopback hostname.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := loopbackAddr(test.In); got != test.Want {
				t.Errorf("test %d: loopbackAddr(%q) = %t, want %t", testNum, test.In, got, test.Want)
			}
		})
	}
}

// TestLocalOnlyServe pins the one configuration that must reject foreign Host
// headers: a loopback bind with no token and no public flag, which is exactly
// where DNS rebinding lands. Everything else legitimately sees proxied Hosts.
func TestLocalOnlyServe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		Addr   string
		Token  string
		Public bool
		Want   bool
	}{{ // Test 0: The rebinding-vulnerable config pins the Host.
		Addr: "127.0.0.1:8765", Want: true,
	}, { // Test 1: A token gates the server on its own, and a TLS proxy in
		// front forwards its public Host.
		Addr: "127.0.0.1:8765", Token: "x", Want: false,
	}, { // Test 2: The public demo behind its proxy sees its public Host.
		Addr: "127.0.0.1:8765", Public: true, Want: false,
	}, { // Test 3: An off-loopback bind already requires a token or public.
		Addr: "0.0.0.0:8765", Want: false,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			if got := localOnlyServe(test.Token, test.Public, test.Addr); got != test.Want {
				t.Errorf("localOnlyServe(%q, %t, %q) = %t, want %t",
					test.Token, test.Public, test.Addr, got, test.Want)
			}
		})
	}
}

// TestAttestFnSeals verifies the web sealer serves a zip that unpacks into a
// directory whotoask verify accepts, and a manifest matching the one inside it.
func TestAttestFnSeals(t *testing.T) {
	t.Parallel()
	opts := &options{dataDir: t.TempDir()}
	sealer := attestFn(index.New(), opts, false, io.Discard)
	if sealer == nil {
		t.Fatal("attestFn returned no sealer")
	}
	sealed, err := sealer()
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(sealed.Archive), int64(len(sealed.Archive)))
	if err != nil {
		t.Fatalf("archive is not a zip: %v", err)
	}
	out := t.TempDir()
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		path := filepath.Join(out, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(out, "finding")
	report, err := seal.Verify(dir, "")
	if err != nil || !report.OK() {
		t.Fatalf("the downloaded seal does not verify: %v %+v", err, report)
	}
	if diff := cmp.Diff([]string{riskJSONName}, report.Verified); diff != "" {
		t.Errorf("verified files mismatch (-want +got):\n%s", diff)
	}
	inside, err := os.ReadFile(filepath.Join(dir, seal.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(inside, sealed.Manifest) {
		t.Error("the served manifest is not the one inside the archive")
	}
}

// TestAttestFnMemoryKeyWritesNothing verifies a sealer asked for a memory key
// signs without creating a signing key in the data directory, which is what
// lets the demo promise it writes nothing there, and that the persistent path
// still creates one.
func TestAttestFnMemoryKeyWritesNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		MemoryKey   bool
		WantKeyFile bool
	}{{ // Test 0: The demo's memory key leaves the data directory untouched.
		MemoryKey: true, WantKeyFile: false,
	}, { // Test 1: Serve keeps one key under the data directory.
		MemoryKey: false, WantKeyFile: true,
	}}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			opts := &options{dataDir: t.TempDir()}
			sealer := attestFn(index.New(), opts, test.MemoryKey, io.Discard)
			if sealer == nil {
				t.Fatal("attestFn returned no sealer")
			}
			if _, err := sealer(); err != nil {
				t.Fatalf("seal: %v", err)
			}
			_, err := os.Stat(opts.dir().AttestKey())
			if got := err == nil; got != test.WantKeyFile {
				t.Errorf("signing key on disk = %t, want %t (stat: %v)", got, test.WantKeyFile, err)
			}
		})
	}
}

// TestRecallFnPublicExemption checks that the demo's public flag lets recall
// serve off loopback, while a normal off-loopback bind or any serve token keeps
// it off. The demo's history is sample data, so it has nothing private to gate.
func TestRecallFnPublicExemption(t *testing.T) {
	t.Parallel()
	store := episode.New()
	store.Add(episode.Episode{ID: "e1"})
	ix := index.New()
	tests := []struct {
		Addr   string
		Token  string
		Public bool
		Want   bool
	}{
		{Addr: "127.0.0.1:8765", Public: false, Want: true},           // Test 0: loopback, no token.
		{Addr: "0.0.0.0:8765", Public: true, Want: true},              // Test 1: public demo off loopback.
		{Addr: "0.0.0.0:8765", Public: false, Want: false},            // Test 2: off loopback, not public.
		{Addr: "0.0.0.0:8765", Token: "x", Public: true, Want: false}, // Test 3: token gates it regardless.
	}
	for testNum, test := range tests {
		t.Run(fmt.Sprintf("test %d", testNum), func(t *testing.T) {
			t.Parallel()
			cfg := webConfig{addr: test.Addr, public: test.Public, episodes: store}
			if got := recallFn(engine.New(ix, policy.Default()), cfg, test.Token) != nil; got != test.Want {
				t.Errorf("recallFn present = %t, want %t", got, test.Want)
			}
		})
	}
}
