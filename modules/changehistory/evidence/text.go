// Package evidence owns chunked filesystem evidence in the selected session store.
// References carry no workspace authority and never read a live workspace path.
package evidence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const (
	ChunkBytes = 1 << 20
	// Text includes a rendered before/after diff, not just an 8 MiB endpoint.
	MaxTextBytes = 64 << 20
	InlineBytes  = 32 << 10
)

var textName = recordkind.HashedName("change-text-", ".bin")
var stagedName = recordkind.HashedName("change-stage-", ".json")

// A fresh, unowned root protects reused chunks until the capture is published.
// It expires through ordinary orphan retention, including abandoned captures.
type stagedText struct {
	Nonce string
	Text  Text
}

// Source borrows an admitted store and its publication authority. It owns no
// directory, worker, cache or lease; the enclosing operation owns their lifetime.
type Source struct {
	Store  *history.Store
	Thread string
	Limits history.Limits
	Writes *persistence.Counter
	Read   func(name string, limit int64) ([]byte, error)
}

func (s *Source) read(name string, limit int64) ([]byte, error) {
	if s != nil {
		if s.Store != nil {
			return s.Store.ReadRetained(name, limit)
		}
		if s.Read != nil {
			return s.Read(name, limit)
		}
	}
	return nil, errors.New("capture evidence reader unavailable")
}

// Text is a bounded value represented by immutable, independently bounded parts.
// The reader is supplied by the admitted consumer, never by persisted metadata.
type Text struct {
	Parts  []string
	Size   int
	Digest string
	source *Source
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func (s *Source) put(tx *history.Transaction, name string, data []byte) error {
	if err := tx.Admit(s.Limits, name, int64(len(data))); err != nil {
		return err
	}
	if err := tx.Write(name, "change-evidence-pending-", data); err != nil {
		return err
	}
	return nil
}

// SaveText stages one value, never a batch. Recent unowned records are protected
// by session history. Write adopts their ownership before publishing any file or
// manifest that references them; late acquisition leaves only expirable staging.
func (s *Source) SaveText(ctx context.Context, value string) (*Text, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("capture evidence storage unavailable")
	}
	if len(value) > MaxTextBytes || !utf8.ValidString(value) {
		return nil, errors.New("text exceeds the per-file evidence bound or is not UTF-8")
	}
	var text *Text
	err := s.Store.Transaction(ctx, s.Writes, func(tx *history.Transaction) error {
		var err error
		text, err = s.saveText(tx, value)
		if err != nil {
			return err
		}
		data, err := json.Marshal(stagedText{Nonce: rand.Text(), Text: *text}, json.Deterministic(true))
		if err != nil {
			return err
		}
		return s.put(tx, "change-stage-"+digest(data)+".json", data)
	})
	return text, err
}

func (s *Source) saveText(tx *history.Transaction, value string) (*Text, error) {
	if len(value) > MaxTextBytes || !utf8.ValidString(value) {
		return nil, errors.New("text exceeds the per-file evidence bound or is not UTF-8")
	}
	text := &Text{Size: len(value), Digest: digest([]byte(value)), source: s}
	for start := 0; start < len(value); start += ChunkBytes {
		data := []byte(value[start:min(start+ChunkBytes, len(value))])
		name := "change-text-" + digest(data) + ".bin"
		if err := s.put(tx, name, data); err != nil {
			return nil, err
		}
		text.Parts = append(text.Parts, name)
	}
	return text, nil
}

func (t *Text) Bind(source *Source) error {
	if t == nil {
		return nil
	}
	if t.Size < 0 || t.Size > MaxTextBytes || len(t.Parts) != (t.Size+ChunkBytes-1)/ChunkBytes || len(t.Digest) != 64 || strings.Trim(t.Digest, "0123456789abcdef") != "" {
		return errors.New("invalid retained text reference")
	}
	for _, part := range t.Parts {
		if !textName(part) {
			return errors.New("invalid retained text part")
		}
	}
	t.source = source
	return nil
}

func (t *Text) part(index int) ([]byte, error) {
	if t == nil || t.source == nil {
		return nil, errors.New("retained text has no admitted reader")
	}
	name := t.Parts[index]
	data, err := t.source.read(name, ChunkBytes)
	if err != nil {
		return nil, fmt.Errorf("retained evidence %s: %w", name, err)
	}
	size := min(ChunkBytes, t.Size-index*ChunkBytes)
	if len(data) != size || "change-text-"+digest(data)+".bin" != name {
		return nil, fmt.Errorf("retained evidence %s has invalid size or content", name)
	}
	return data, nil
}

// Read materializes exactly one file's text. Callers discard it before advancing
// to the next file; collections retain only references and path metadata.
func (t *Text) Read() (string, error) {
	if t == nil {
		return "", nil
	}
	var value strings.Builder
	value.Grow(t.Size)
	for i := range t.Parts {
		data, err := t.part(i)
		if err != nil {
			return "", err
		}
		value.Write(data)
	}
	text := value.String()
	if digest([]byte(text)) != t.Digest || !utf8.ValidString(text) {
		return "", errors.New("invalid retained text digest or encoding")
	}
	return text, nil
}

// Prefix is for bounded detached receipts, never composition or mutation.
func (t *Text) Prefix(limit int) (string, error) {
	var value strings.Builder
	for i := range t.Parts {
		if value.Len() >= limit {
			break
		}
		data, err := t.part(i)
		if err != nil {
			return "", err
		}
		value.Write(data[:min(len(data), limit-value.Len())])
	}
	text := value.String()
	for len(text) > 0 && !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, nil
}

// Verify checks selected immutable text without assembling it. Count-only
// consumers still report missing/corrupt evidence rather than complete results.
func (t *Text) Verify() error {
	if t == nil {
		return nil
	}
	hash := sha256.New()
	for i := range t.Parts {
		data, err := t.part(i)
		if err != nil {
			return err
		}
		_, _ = hash.Write(data)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != t.Digest {
		return errors.New("invalid retained text digest")
	}
	return nil
}
