// Persisting the index: the snapshot on disk, optionally sealed, and the
// load path that rebuilds everything derived.

package index

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/identity"
	"github.com/dcadolph/peepwho/internal/invindex"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
	"github.com/dcadolph/peepwho/internal/vault"
)

// snapshotSchema is the snapshot layout this build writes and requires on read.
// It moves whenever the shape of the snapshot or the sidecar changes meaning,
// so a file from a different layout is refused with its version named rather
// than decoded best-effort into a partial index.
const snapshotSchema = 1

// snapshot is the serializable form of an index written to and read from disk.
type snapshot struct {
	// Schema is the snapshot layout version; Load refuses any other value.
	Schema int `json:"schema"`
	// Graph is the entity graph.
	Graph *model.Graph `json:"graph"`
	// Postings is the per-person inverted index packed as a compact binary blob
	// (JSON stores a byte slice as base64), which is far smaller and faster to
	// read than a map of maps and is the bulk of an index once the source
	// records move to the sidecar.
	Postings []byte `json:"postings"`
	// Texts holds normalized per-person field text.
	Texts map[model.ID]*personText `json:"texts"`
	// ChannelPostings is the per-channel inverted index, packed the same way.
	ChannelPostings []byte `json:"channel_postings"`
	// ChannelTexts holds normalized per-channel field text.
	ChannelTexts map[model.ID]*channelText `json:"channel_texts"`
	// PersonVecs holds per-person embedding vectors, quantized to int8 (JSON
	// stores each as a small number array), a quarter the size of float32.
	PersonVecs map[model.ID][]int8 `json:"person_vecs,omitempty"`
	// ChannelVecs holds per-channel embedding vectors, quantized the same way.
	ChannelVecs map[model.ID][]int8 `json:"channel_vecs,omitempty"`
	// TopicVecs holds per-topic embedding vectors, quantized the same way.
	TopicVecs map[model.ID][]int8 `json:"topic_vecs,omitempty"`
	// Aliases maps each known alias identifier to its canonical form.
	Aliases map[model.ID]model.ID `json:"aliases,omitempty"`
	// Joins records the inferred identity merges with their strength and
	// evidence, so a re-index keeps them and a reader can audit why two
	// identities became one.
	Joins []Join `json:"joins,omitempty"`
	// SourceCounts is how many records each source contributed. The records
	// themselves live in a sidecar file so a query never loads them; only their
	// counts stay here for status and the shrink guard.
	SourceCounts map[string]int `json:"source_counts,omitempty"`
	// BuiltAt is when the index was last written, so a reader can tell how stale
	// it is without re-running an index.
	BuiltAt time.Time `json:"built_at,omitempty"`
}

// sourcesSnapshot is the sidecar that holds the raw records per source, read
// only when a merge needs to rebuild. Keeping it out of the main index is what
// lets a query load a fraction of the bytes, since the records are the bulk of
// an index and no query reads them.
type sourcesSnapshot struct {
	// Schema is the snapshot layout version; LoadSources refuses any other value.
	Schema int `json:"schema"`
	// Sources holds the records each source contributed, so a later merge can
	// replace one source without re-reading the others.
	Sources map[string][]connector.Record `json:"sources,omitempty"`
}

// Option configures New, Load, and Save. With no option the index is read and
// written as plain JSON and reads the system clock; WithCodec injects an at-rest
// codec so the file is encrypted, and WithNow injects the clock.
type Option func(*ioConfig)

// ioConfig holds the resolved options for one New, Load, or Save.
type ioConfig struct {
	// codec transforms the bytes at rest; Plain by default.
	codec vault.Codec
	// now is the clock the index measures recency and its build time by.
	now func() time.Time
}

// newIOConfig applies opts over the plain-JSON, system-clock default.
func newIOConfig(opts []Option) ioConfig {
	cfg := ioConfig{codec: vault.Plain{}, now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// WithCodec sets the at-rest codec for a Load or Save. A nil codec is ignored,
// leaving the plain-JSON default.
func WithCodec(c vault.Codec) Option {
	return func(cfg *ioConfig) {
		if c != nil {
			cfg.codec = c
		}
	}
}

// WithNow sets the clock a new or loaded index reads for recency decay and for
// the build time Save records. A nil clock is ignored, leaving the system clock.
func WithNow(now func() time.Time) Option {
	return func(cfg *ioConfig) {
		if now != nil {
			cfg.now = now
		}
	}
}

// Save writes the index to path readable only by the owner (mode 0600), creating
// parent directories as needed. It is compact JSON, or its encrypted form when
// WithCodec is set, and each write goes through a temporary file and a rename so
// a crash cannot truncate an existing file. The raw source records, which are
// the bulk of an index and which no query reads, go to a sidecar file next to
// path so a query loads only the small main index.
func (ix *Index) Save(path string, opts ...Option) error {
	cfg := newIOConfig(opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("index: mkdir: %w", err)
	}
	// The index and its sidecar only mean anything as a pair, and each write is
	// atomic on its own but the pair is not: the caller's cross-process lock on
	// the index path serializes whole runs, and Save must not take that same
	// lock itself, since flock on a second descriptor blocks even within one
	// process. A crash between the two renames still leaves a torn pair with
	// nobody holding any lock, which is why LoadSources checks the counts.
	snap := snapshot{
		Schema:          snapshotSchema,
		Graph:           ix.Graph,
		Postings:        invindex.EncodePostings(ix.postings),
		Texts:           ix.texts,
		ChannelPostings: invindex.EncodePostings(ix.channelPostings),
		ChannelTexts:    ix.channelTexts,
		PersonVecs:      quantizeVecs(ix.personVecs),
		ChannelVecs:     quantizeVecs(ix.channelVecs),
		TopicVecs:       quantizeVecs(ix.topicVecs),
		Aliases:         ix.identityResolver().Pairs(),
		Joins:           ix.joins,
		SourceCounts:    ix.sourceCounts,
		BuiltAt:         ix.now(),
	}
	if err := writeEncoded(path, snap, cfg.codec); err != nil {
		return err
	}
	// Write the sources sidecar only when the sources are in hand. An index
	// loaded to answer a query carries none, and overwriting the sidecar then
	// would erase the records a later merge needs; leaving it keeps them.
	if ix.sources != nil {
		side := sourcesSnapshot{Schema: snapshotSchema, Sources: redactedSources(ix.sources)}
		if err := writeEncoded(SourcesPath(path), side, cfg.codec); err != nil {
			return err
		}
	}
	return nil
}

// checkSchema verifies raw carries the snapshot layout this build reads,
// naming both versions when it does not. It decodes only the version field, so
// a snapshot whose other fields no longer even parse still reports the real
// cause instead of ErrDamaged.
func checkSchema(raw []byte, name string) error {
	var ver struct {
		// Schema is the layout version stamped by the build that wrote the file.
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &ver); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrDamaged, name, err)
	}
	if ver.Schema != snapshotSchema {
		return fmt.Errorf("%w: %s holds schema %d and this build reads %d; re-index to rebuild it",
			ErrSchema, name, ver.Schema, snapshotSchema)
	}
	return nil
}

// writeEncoded marshals v to JSON, encodes it with the codec, and writes it
// atomically at mode 0600.
func writeEncoded(path string, v any, codec vault.Codec) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("index: encode %s: %w", filepath.Base(path), err)
	}
	enc, err := codec.Encode(raw)
	if err != nil {
		return fmt.Errorf("index: encrypt %s: %w", filepath.Base(path), err)
	}
	if err := util.WriteFileAtomic(path, enc, 0o600); err != nil {
		return fmt.Errorf("index: write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// SourcesPath returns the sidecar path for an index at path: the same name with a
// .sources segment inserted, so the two files travel together.
func SourcesPath(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".sources" + ext
}

// Load reads an index previously written by Save, decrypting it when WithCodec
// supplies the key. It returns vault.ErrEncrypted when the file is encrypted but
// no codec is given, so a caller can prompt for a passphrase.
func Load(path string, opts ...Option) (*Index, error) {
	cfg := newIOConfig(opts)
	stored, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("index: open: %w", err)
	}
	raw, err := cfg.codec.Decode(stored)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	if err := checkSchema(raw, filepath.Base(path)); err != nil {
		return nil, err
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrDamaged, filepath.Base(path), err)
	}
	postings, err := invindex.DecodePostings[model.ID](snap.Postings)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	channelPostings, err := invindex.DecodePostings[model.ID](snap.ChannelPostings)
	if err != nil {
		return nil, fmt.Errorf("index: channel %w", err)
	}
	ix := &Index{
		Graph:           snap.Graph,
		postings:        postings,
		texts:           snap.Texts,
		channelPostings: channelPostings,
		channelTexts:    snap.ChannelTexts,
		personVecs:      dequantizeVecs(snap.PersonVecs),
		channelVecs:     dequantizeVecs(snap.ChannelVecs),
		topicVecs:       dequantizeVecs(snap.TopicVecs),
		// sources stays nil: a loaded index answers queries, which never read
		// the records. A merge calls LoadSources to bring them in from the
		// sidecar before rebuilding.
		sourceCounts: snap.SourceCounts,
		builtAt:      snap.BuiltAt,
		resolver:     identity.NewResolver(),
		joins:        snap.Joins,
		halfLife:     DefaultHalfLife,
		now:          cfg.now,
	}
	ix.resolver.Restore(snap.Aliases)
	if ix.Graph == nil {
		ix.Graph = model.NewGraph()
	}
	if ix.Graph.People == nil {
		ix.Graph.People = make(map[model.ID]*model.Person)
	}
	if ix.Graph.Teams == nil {
		ix.Graph.Teams = make(map[model.ID]*model.Team)
	}
	if ix.Graph.Orgs == nil {
		ix.Graph.Orgs = make(map[model.ID]*model.Org)
	}
	if ix.Graph.Topics == nil {
		ix.Graph.Topics = make(map[model.ID]*model.Topic)
	}
	if ix.Graph.Channels == nil {
		ix.Graph.Channels = make(map[model.ID]*model.Channel)
	}
	if ix.postings == nil {
		ix.postings = make(map[string]map[model.ID]float64)
	}
	if ix.texts == nil {
		ix.texts = make(map[model.ID]*personText)
	}
	if ix.channelPostings == nil {
		ix.channelPostings = make(map[string]map[model.ID]float64)
	}
	if ix.channelTexts == nil {
		ix.channelTexts = make(map[model.ID]*channelText)
	}
	if ix.personVecs == nil {
		ix.personVecs = make(map[model.ID][]float32)
	}
	if ix.topicVecs == nil {
		ix.topicVecs = make(map[model.ID][]float32)
	}
	if ix.channelVecs == nil {
		ix.channelVecs = make(map[model.ID][]float32)
	}
	ix.refreshStats()
	return ix, nil
}

// LoadSources reads the sources sidecar for the index at path into the index, so
// a merge can rebuild from every source and not just the one it is adding.
// Merging into a loaded index must call this first: without the records a
// rebuild would drop every source read before. A missing sidecar is an error
// rather than an empty set, since a silent shrink is exactly what this guards.
func (ix *Index) LoadSources(path string, opts ...Option) error {
	cfg := newIOConfig(opts)
	stored, err := os.ReadFile(SourcesPath(path))
	if err != nil {
		return fmt.Errorf("index: open sources: %w", err)
	}
	raw, err := cfg.codec.Decode(stored)
	if err != nil {
		return fmt.Errorf("index: sources: %w", err)
	}
	if err := checkSchema(raw, filepath.Base(SourcesPath(path))); err != nil {
		return err
	}
	var side sourcesSnapshot
	if err := json.Unmarshal(raw, &side); err != nil {
		return fmt.Errorf("index: decode sources: %w", err)
	}
	// The snapshot's own counts say what the sidecar must hold. A sidecar that
	// disagrees came from a different run: a crash between the two writes, or a
	// concurrent index run whose pair interleaved with this one. Rebuilding from
	// it would silently revert every source to the older records, so the
	// mismatch is refused with each source named.
	if ix.sourceCounts != nil {
		if err := matchSidecar(ix.sourceCounts, side.Sources); err != nil {
			return err
		}
	}
	ix.sources = side.Sources
	if ix.sources == nil {
		ix.sources = make(map[string][]connector.Record)
	}
	if ix.sourceCounts == nil {
		ix.sourceCounts = make(map[string]int)
	}
	for name, recs := range ix.sources {
		ix.sourceCounts[name] = len(recs)
	}
	return nil
}

// matchSidecar compares the record counts the index persisted against what the
// sidecar actually holds and reports every source that disagrees, by name, so
// the operator sees exactly which reads would be lost by trusting the pair.
func matchSidecar(counts map[string]int, sources map[string][]connector.Record) error {
	names := make(map[string]bool, len(counts)+len(sources))
	for name := range counts {
		names[name] = true
	}
	for name := range sources {
		names[name] = true
	}
	var bad []string
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if counts[name] != len(sources[name]) {
			bad = append(bad, fmt.Sprintf("%s holds %d records in the sidecar and %d in the index",
				name, len(sources[name]), counts[name]))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s; re-index to rebuild the pair", ErrStaleSidecar, strings.Join(bad, ", "))
}
