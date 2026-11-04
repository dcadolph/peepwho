package episode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dcadolph/peepwho/internal/invindex"
	"github.com/dcadolph/peepwho/internal/model"
	"github.com/dcadolph/peepwho/internal/util"
	"github.com/dcadolph/peepwho/internal/vault"
	"github.com/dcadolph/peepwho/internal/vector"
)

// snapshot is the serializable form of a store. The participant index is
// derived on load rather than stored, so it can never disagree with the
// episodes themselves.
type snapshot struct {
	// IDs is the interned table of person ids the episodes reference. A person
	// who took part in a thousand conversations is written here once, and each
	// packed episode names them by index. Repeated ids were most of a store's
	// bulk before interning, after the retained archive itself.
	IDs []model.ID `json:"ids,omitempty"`
	// Episodes are the stored episodes in packed form, their participant and
	// archived-author ids replaced by indices into IDs.
	Episodes []packedEpisode `json:"episodes"`
	// Postings is the term-to-per-episode inverted index, packed as a compact
	// binary blob (JSON stores a byte slice as base64) rather than a JSON map of
	// maps, which is far smaller and faster to read back.
	Postings []byte `json:"postings"`
	// Vecs holds per-episode embedding vectors, quantized to int8, a quarter the
	// size of float32 and the largest part of a store once episodes are embedded.
	Vecs map[string][]int8 `json:"vecs,omitempty"`
}

// packedEpisode is the on-disk form of an Episode. It mirrors the domain type
// field for field except that participants and archived-note authors are held
// as indices into the snapshot id table rather than repeated id strings. A
// field added to Episode that must persist has to be added here too; Body is
// deliberately absent, since it is never written to disk.
type packedEpisode struct {
	// ID uniquely identifies the episode.
	ID string `json:"id"`
	// Source names the origin connector.
	Source string `json:"source"`
	// Kind classifies the conversation shape.
	Kind Kind `json:"kind"`
	// Place is the human-readable location.
	Place string `json:"place"`
	// PlaceID is the source's own identifier for that location.
	PlaceID string `json:"place_id,omitempty"`
	// Title is a short subject line when the source has one.
	Title string `json:"title,omitempty"`
	// Participants are the people who took part, as indices into the snapshot id
	// table, most involved first.
	Participants []uint32 `json:"participants"`
	// Asked is the index of the person who opened the conversation, held one
	// past their place in the snapshot id table so that zero means the source
	// knew of no asker. An index is not nullable, and an episode with no asker
	// is the common case, so the offset keeps it out of the file entirely.
	Asked uint32 `json:"asked,omitempty"`
	// Occurred is when the conversation last saw activity.
	Occurred time.Time `json:"occurred"`
	// Permalink points back to the conversation in its own tool.
	Permalink string `json:"permalink,omitempty"`
	// Messages counts the messages the episode was built from.
	Messages int `json:"messages,omitempty"`
	// Archive holds retained conversation content, its authors interned.
	Archive []packedNote `json:"archive,omitempty"`
}

// packedNote is the on-disk form of a Note, its author held as an index into
// the snapshot id table.
type packedNote struct {
	// Author is the index of the canonical writer in the snapshot id table.
	Author uint32 `json:"author"`
	// At is when it was written.
	At time.Time `json:"at"`
	// Text is the message body as written.
	Text string `json:"text"`
}

// Option configures Load and Save. With no option the store is read and
// written as plain JSON; WithCodec injects an at-rest codec so the file is
// encrypted with the same key as the index.
type Option func(*ioConfig)

// ioConfig holds the resolved options for one Load or Save.
type ioConfig struct {
	// codec transforms the bytes at rest; Plain by default.
	codec vault.Codec
}

// newIOConfig applies opts over the plain-JSON default.
func newIOConfig(opts []Option) ioConfig {
	cfg := ioConfig{codec: vault.Plain{}}
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

// Save writes the store to path readable only by the owner (mode 0600),
// creating parent directories as needed, through a temporary file and a rename
// so a crash cannot truncate an existing file.
func (s *Store) Save(path string, opts ...Option) error {
	cfg := newIOConfig(opts)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("episode: mkdir: %w", err)
	}
	ids, packed := packEpisodes(s.All())
	snap := snapshot{
		IDs:      ids,
		Episodes: packed,
		Postings: invindex.EncodePostings(s.postings),
		Vecs:     quantizeEpisodeVecs(s.vecs),
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("episode: encode: %w", err)
	}
	enc, err := cfg.codec.Encode(raw)
	if err != nil {
		return fmt.Errorf("episode: encrypt: %w", err)
	}
	if err := util.WriteFileAtomic(path, enc, 0o600); err != nil {
		return fmt.Errorf("episode: write: %w", err)
	}
	return nil
}

// Load reads a store previously written by Save, decrypting it when WithCodec
// supplies the key. It returns vault.ErrEncrypted when the file is encrypted
// but no codec is given, so a caller can prompt for a passphrase.
func Load(path string, opts ...Option) (*Store, error) {
	cfg := newIOConfig(opts)
	stored, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("episode: open: %w", err)
	}
	raw, err := cfg.codec.Decode(stored)
	if err != nil {
		return nil, fmt.Errorf("episode: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("episode: decode: %w", err)
	}
	s := New()
	postings, err := invindex.DecodePostings[string](snap.Postings)
	if err != nil {
		return nil, fmt.Errorf("episode: %w", err)
	}
	if len(postings) > 0 {
		s.postings = postings
		// Which terms belong to which episode is not stored, but inverting the
		// postings once on load costs a single pass and spares every later
		// removal from walking the whole vocabulary.
		s.rebuildTerms()
	}
	if len(snap.Vecs) > 0 {
		s.vecs = dequantizeEpisodeVecs(snap.Vecs)
	}
	for _, pe := range snap.Episodes {
		ep, err := unpackEpisode(pe, snap.IDs)
		if err != nil {
			return nil, fmt.Errorf("episode: %w", err)
		}
		if ep.ID == "" {
			continue
		}
		s.episodes[ep.ID] = ep
		for _, p := range ep.Participants {
			s.byParticipant[p] = append(s.byParticipant[p], ep.ID)
		}
	}
	return s, nil
}

// idTable interns person ids to indices while saving, so an id shared by many
// episodes is written to the snapshot once.
type idTable struct {
	// ids is the interned list in first-seen order.
	ids []model.ID
	// index maps an id to its position in ids.
	index map[model.ID]uint32
}

// newIDTable returns an empty interner.
func newIDTable() *idTable {
	return &idTable{index: make(map[model.ID]uint32)}
}

// intern returns the index of id, appending it on first sight.
func (t *idTable) intern(id model.ID) uint32 {
	if i, ok := t.index[id]; ok {
		return i
	}
	i := uint32(len(t.ids))
	t.ids = append(t.ids, id)
	t.index[id] = i
	return i
}

// packEpisodes converts episodes to their on-disk form and returns the shared
// id table their indices refer to. Participants, askers, and archived-note
// authors are interned into the same table in the order the episodes are
// given, so the output is deterministic.
func packEpisodes(eps []*Episode) ([]model.ID, []packedEpisode) {
	t := newIDTable()
	packed := make([]packedEpisode, 0, len(eps))
	for _, ep := range eps {
		if ep == nil {
			continue
		}
		parts := make([]uint32, len(ep.Participants))
		for i, p := range ep.Participants {
			parts[i] = t.intern(p)
		}
		var archive []packedNote
		if len(ep.Archive) > 0 {
			archive = make([]packedNote, len(ep.Archive))
			for i, n := range ep.Archive {
				archive[i] = packedNote{Author: t.intern(n.Author), At: n.At, Text: n.Text}
			}
		}
		var asked uint32
		if ep.Asked != "" {
			asked = t.intern(ep.Asked) + 1
		}
		packed = append(packed, packedEpisode{
			ID:           ep.ID,
			Source:       ep.Source,
			Kind:         ep.Kind,
			Place:        ep.Place,
			PlaceID:      ep.PlaceID,
			Title:        ep.Title,
			Participants: parts,
			Asked:        asked,
			Occurred:     ep.Occurred,
			Permalink:    ep.Permalink,
			Messages:     ep.Messages,
			Archive:      archive,
		})
	}
	return t.ids, packed
}

// unpackEpisode restores an episode from its on-disk form, resolving each
// interned index against ids. An index past the end of the table means a
// truncated or corrupt file and is reported rather than silently dropped.
func unpackEpisode(pe packedEpisode, ids []model.ID) (*Episode, error) {
	parts := make([]model.ID, len(pe.Participants))
	for i, idx := range pe.Participants {
		p, err := idAt(ids, idx)
		if err != nil {
			return nil, err
		}
		parts[i] = p
	}
	var asked model.ID
	if pe.Asked > 0 {
		p, err := idAt(ids, pe.Asked-1)
		if err != nil {
			return nil, err
		}
		asked = p
	}
	var archive []Note
	if len(pe.Archive) > 0 {
		archive = make([]Note, len(pe.Archive))
		for i, n := range pe.Archive {
			author, err := idAt(ids, n.Author)
			if err != nil {
				return nil, err
			}
			archive[i] = Note{Author: author, At: n.At, Text: n.Text}
		}
	}
	return &Episode{
		ID:           pe.ID,
		Source:       pe.Source,
		Kind:         pe.Kind,
		Place:        pe.Place,
		PlaceID:      pe.PlaceID,
		Title:        pe.Title,
		Participants: parts,
		Asked:        asked,
		Occurred:     pe.Occurred,
		Permalink:    pe.Permalink,
		Messages:     pe.Messages,
		Archive:      archive,
	}, nil
}

// idAt returns the id at idx, or an error when idx is out of range.
func idAt(ids []model.ID, idx uint32) (model.ID, error) {
	if int(idx) >= len(ids) {
		return "", fmt.Errorf("person index %d out of range %d", idx, len(ids))
	}
	return ids[idx], nil
}

// LoadOrNew reads a store from path, returning an empty store when the file
// does not exist. Every other failure, including a missing decryption key, is
// returned so a real problem is never mistaken for an empty history.
func LoadOrNew(path string, opts ...Option) (*Store, error) {
	s, err := Load(path, opts...)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return New(), nil
		}
		return nil, err
	}
	return s, nil
}

// PurgeBefore drops episodes that last saw activity before cutoff, returning
// how many were removed. It is how a retention window is enforced.
func (s *Store) PurgeBefore(cutoff time.Time) int {
	var stale []*Episode
	for _, ep := range s.episodes {
		if !ep.Occurred.IsZero() && ep.Occurred.Before(cutoff) {
			stale = append(stale, ep)
		}
	}
	for _, ep := range stale {
		s.forget(ep)
	}
	return len(stale)
}

// PurgeArchive drops retained conversation content from every episode, leaving
// the pointers intact. It is what turning the archive off does to history
// already on disk.
func (s *Store) PurgeArchive() int {
	n := 0
	for _, ep := range s.episodes {
		if ep.Archived() {
			ep.Archive = nil
			// The archive was the searchable body, so drop this episode's
			// postings and vector too. Otherwise purged content still drives
			// keyword and semantic search while the words themselves are gone.
			s.dropSearchIndex(ep.ID)
			n++
		}
	}
	return n
}

// Relink replaces an episode's participants and asker and repoints the lookup
// that finds it by person. It is how an identity join reaches conversations
// already stored.
//
// The asker travels with the participants rather than through a setter of its
// own. Rewritten separately they drift: an asker left under a source-native id
// while the participants became canonical matches none of them, and Answered
// then excludes nobody and hands back the whole list, crediting the person who
// asked with answering themselves. One call rewrites both or neither.
func (s *Store) Relink(id string, participants []model.ID, asked model.ID) {
	ep, ok := s.episodes[id]
	if !ok {
		return
	}
	for _, p := range ep.Participants {
		ids := s.byParticipant[p]
		kept := make([]string, 0, len(ids))
		for _, existing := range ids {
			if existing != id {
				kept = append(kept, existing)
			}
		}
		if len(kept) == 0 {
			delete(s.byParticipant, p)
			continue
		}
		s.byParticipant[p] = kept
	}
	ep.Participants = participants
	ep.Asked = asked
	for _, p := range participants {
		s.byParticipant[p] = append(s.byParticipant[p], id)
	}
}

// HasPerson reports whether any episode includes a person.
func (s *Store) HasPerson(id model.ID) bool { return len(s.byParticipant[id]) > 0 }

// Participants returns the people known to have taken part in any episode.
func (s *Store) Participants() []model.ID {
	out := make([]model.ID, 0, len(s.byParticipant))
	for p := range s.byParticipant {
		out = append(out, p)
	}
	return out
}

// quantizeEpisodeVecs compresses each episode vector to int8 for compact storage.
func quantizeEpisodeVecs(vecs map[string][]float32) map[string][]int8 {
	if len(vecs) == 0 {
		return nil
	}
	out := make(map[string][]int8, len(vecs))
	for id, v := range vecs {
		out[id] = vector.Quantize(v)
	}
	return out
}

// dequantizeEpisodeVecs restores int8 episode vectors to float32 for scoring.
func dequantizeEpisodeVecs(vecs map[string][]int8) map[string][]float32 {
	out := make(map[string][]float32, len(vecs))
	for id, q := range vecs {
		out[id] = vector.Dequantize(q)
	}
	return out
}
