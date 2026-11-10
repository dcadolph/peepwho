package cmd

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/resolve"
	"github.com/dcadolph/whotoask/internal/seal"
)

// riskJSONName is the file a bare attestation writes the scored report to.
const riskJSONName = "risk.json"

// newAttestCmd builds the attest command: the knowledge-risk finding written to
// a directory with a signed manifest beside it, so a board, auditor, or acquirer
// can trust the finding without trusting the machine that produced it.
func newAttestCmd(opts *options) *cobra.Command {
	var outDir string
	cmd := &cobra.Command{
		Use:   "attest",
		Short: "Seal the knowledge-risk finding into a directory anyone can verify offline",
		Long: `Write the current knowledge-risk finding to risk.json in a directory, with a
signed manifest beside it. manifest.json lists every file by size and sha256,
and manifest.sig is an ed25519 signature over the exact bytes of manifest.json.
Anyone can check it offline with whotoask verify, with no account and nothing
sent anywhere.

The signing key is created once under the data directory and reused. Publish
the output of whotoask attest key somewhere a reader trusts, so they can pin it
with whotoask verify --key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ix, err := opts.loadIndex(cmd)
			if err != nil {
				return noIndexError(err)
			}
			priv, err := opts.attestKey()
			if err != nil {
				return err
			}
			m, err := sealRiskDir(outDir, priv, ix, resolve.Risk(ix, 0), opts.now())
			if err != nil {
				return err
			}
			view := sealSummary{Dir: outDir, KeyID: m.KeyID, Files: len(m.Files)}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				fmt.Fprintf(w, "%s %d file%s into %s with key %s\n",
					s.Accent("sealed:"), view.Files, plural(view.Files), view.Dir, view.KeyID)
				fmt.Fprintf(w, "%s\n", s.Dim("check it with: whotoask verify "+view.Dir))
				renderNotFor(w, s)
			})
		},
	}
	cmd.Flags().StringVar(&outDir, "out", "finding", "Directory to write the sealed finding into.")
	cmd.AddCommand(newAttestKeyCmd(opts))
	return cmd
}

// sealSummary is what attest reports after writing a seal.
type sealSummary struct {
	// Dir is the directory the seal was written into.
	Dir string `json:"dir"`
	// KeyID is the key the manifest was signed with.
	KeyID string `json:"key_id"`
	// Files is how many files the seal covers.
	Files int `json:"files"`
}

// newAttestKeyCmd builds the key subcommand, which prints this install's public
// sealing key for publishing out of band.
func newAttestKeyCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "key",
		Short: "Print this install's public sealing key and key id",
		Long: `Print the public key this install signs seals with, and its key id. Publish
it somewhere a reader already trusts. A reader who passes it to whotoask verify
--key knows the seal came from this install and not from anyone who can run
whotoask.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			priv, err := opts.attestKey()
			if err != nil {
				return err
			}
			pub, ok := priv.Public().(ed25519.PublicKey)
			if !ok {
				return fmt.Errorf("attest: key has no ed25519 public half")
			}
			view := struct {
				// KeyID is the short identifier of the key.
				KeyID string `json:"key_id"`
				// PublicKey is the base64 ed25519 public key.
				PublicKey string `json:"public_key"`
			}{KeyID: seal.KeyID(pub), PublicKey: base64.StdEncoding.EncodeToString(pub)}
			return opts.render(cmd.OutOrStdout(), view, func(w io.Writer, s style) {
				fmt.Fprintf(w, "%s  %s\n%s  %s\n",
					s.Dim("key id    "), view.KeyID, s.Dim("public key"), view.PublicKey)
			})
		},
	}
}

// sealRiskDir writes the scored report to risk.json in dir and seals it.
func sealRiskDir(
	dir string, priv ed25519.PrivateKey, ix *index.Index, risks []resolve.TopicRisk, now time.Time,
) (seal.Manifest, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return seal.Manifest{}, fmt.Errorf("attest: %w", err)
	}
	data, err := json.MarshalIndent(risks, "", "  ")
	if err != nil {
		return seal.Manifest{}, fmt.Errorf("attest: encode report: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, riskJSONName), append(data, '\n'), 0o644); err != nil {
		return seal.Manifest{}, fmt.Errorf("attest: %w", err)
	}
	return sealFinding(dir, priv, ix, risks, []seal.File{
		{Path: riskJSONName, MediaType: "application/json", Role: "report"},
	}, now)
}

// sealFinding seals files already written into dir under the knowledge-risk
// claim, stamped with now. The seal reads each file back from disk, so it covers
// the bytes that shipped.
func sealFinding(
	dir string, priv ed25519.PrivateKey, ix *index.Index, risks []resolve.TopicRisk, files []seal.File,
	now time.Time,
) (seal.Manifest, error) {
	payload, err := json.Marshal(attestPayload(risks))
	if err != nil {
		return seal.Manifest{}, fmt.Errorf("attest: encode claim: %w", err)
	}
	return seal.Write(dir, seal.Manifest{
		Product:        "whotoask",
		ProductVersion: version,
		CreatedAt:      now.UTC().Format(time.RFC3339),
		Subject:        riskSubject(ix),
		Claim:          seal.Claim{Type: "whotoask.knowledge-risk/1", Payload: payload},
		Files:          files,
	}, priv)
}

// zipSealed packs a sealed directory into a zip under a finding/ folder: the
// manifest, its signature, and every file the manifest covers.
func zipSealed(dir string, m seal.Manifest) ([]byte, error) {
	names := []string{seal.ManifestName, seal.SignatureName}
	for _, f := range m.Files {
		names = append(names, f.Path)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return nil, fmt.Errorf("attest: %w", err)
		}
		w, err := zw.Create("finding/" + name)
		if err != nil {
			return nil, fmt.Errorf("attest: zip: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return nil, fmt.Errorf("attest: zip: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("attest: zip: %w", err)
	}
	return buf.Bytes(), nil
}

// riskSubject names what a knowledge-risk seal is about: the organization the
// index describes, identified by the sources it was read from, so seals from
// two different indexes do not claim the same subject.
func riskSubject(ix *index.Index) seal.Subject {
	id := strings.Join(ix.SourceNames(), "+")
	if id == "" {
		id = "unnamed"
	}
	return seal.Subject{Type: "org", ID: "org:" + id}
}

// attestPayload summarizes the risk report as the claim payload.
func attestPayload(report []resolve.TopicRisk) any {
	critical, sole := 0, 0
	var top []any
	for _, t := range report {
		if len(t.Experts) == 1 {
			sole++
		}
		if t.Level != "critical" {
			continue
		}
		critical++
		if len(top) < 10 {
			expert := t.Lead
			if expert == "" && len(t.Experts) > 0 {
				expert = t.Experts[0].Name
			}
			top = append(top, map[string]any{"topic": t.Topic, "bus_factor": t.BusFactor, "sole_expert": expert})
		}
	}
	payload := map[string]any{
		"finding":       "knowledge concentration",
		"topics_scored": len(report),
		"critical":      critical,
		// How many subjects have one person in the record and nobody else, which
		// is the plainest reading of the finding and the one a reader counts for
		// themselves out of the files beside the seal.
		"single_holder": sole,
		"top_critical":  top,
	}
	return payload
}

// attestKey loads the ed25519 signing key from the data directory, creating it
// on first use. It is the persistent identity every attestation is signed with.
func (o *options) attestKey() (ed25519.PrivateKey, error) {
	path := o.dir().AttestKey()
	if seed, err := os.ReadFile(path); err == nil && len(seed) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(seed), nil
	}
	fresh := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("attest: generate key: %w", err)
	}
	// Name the directory in both failures. The default sits under the home
	// directory, and a hardened service unit usually cannot reach that, so the
	// message has to say which path was refused or the remedy is invisible.
	if err := os.MkdirAll(o.dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("attest: cannot create %s: %w", o.dataDir, err)
	}
	if err := os.WriteFile(path, fresh, 0o600); err != nil {
		return nil, fmt.Errorf("attest: cannot save the key in %s: %w", o.dataDir, err)
	}
	return ed25519.NewKeyFromSeed(fresh), nil
}
