package history

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
)

// FrozenFileRecord registers immutable read-only retained files (mode 0o444)
// published in the store directory for direct reads (for example via mcat or
// host-side reload hydration).
var FrozenFileRecord = recordkind.Spec{ID: "frozen-file", Match: FrozenFileName}

// FrozenResourceRecord registers internal Go-to-Go frozen JSON records
// retained in the thread catalog.
var FrozenResourceRecord = recordkind.Spec{ID: "frozen-resource", Match: FrozenResourceName}

func validFrozenKind(kind string) bool {
	if len(kind) == 0 || len(kind) > 32 {
		return false
	}
	if kind[0] == '-' || kind[len(kind)-1] == '-' {
		return false
	}
	for i := range len(kind) {
		c := kind[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// FrozenFileName recognizes immutable retained files named
// frozen-<kind>-<64-hex>.(txt|json).
func FrozenFileName(name string) bool {
	rest, ok := strings.CutPrefix(name, "frozen-")
	if !ok {
		return false
	}
	switch {
	case strings.HasSuffix(rest, ".txt"):
		rest = strings.TrimSuffix(rest, ".txt")
	case strings.HasSuffix(rest, ".json"):
		rest = strings.TrimSuffix(rest, ".json")
	default:
		return false
	}
	kind, hash, ok := strings.CutLast(rest, "-")
	return ok && validFrozenKind(kind) && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}

// FrozenResourceName recognizes internal frozen JSON resources named
// resource-<64-hex>.json.
var FrozenResourceName = recordkind.HashedName("resource-", ".json")

// WriteReadOnlyFile atomically publishes an immutable read-only (0o444) file in
// the store directory and retains its filename in the thread's session catalog.
func (s *Store) WriteReadOnlyFile(ctx context.Context, thread string, limits Limits, writes *persistence.Counter, kind, ext string, data []byte) (string, string, error) {
	if s == nil {
		return "", "", errors.New("retained storage is unavailable")
	}
	if !validFrozenKind(kind) || (ext != ".txt" && ext != ".json") {
		return "", "", errors.New("invalid frozen file kind or extension")
	}
	if len(data) > MaxRecordBytes {
		return "", "", capacityError("retained frozen file", int64(len(data)), MaxRecordBytes, "Reduce the retained output before retrying.")
	}
	if limits.Total <= 0 {
		limits.Total = DefaultStorageBytes
	}
	name := fmt.Sprintf("frozen-%s-%x%s", kind, sha256.Sum256(data), ext)
	if err := s.Transaction(ctx, writes, func(tx *Transaction) error {
		if err := tx.Admit(limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.WriteReadOnly(name, "frozen-pending-", data); err != nil {
			return err
		}
		return tx.Retain(thread, limits, name)
	}); err != nil {
		return "", "", err
	}
	return filepath.Join(s.directory, name), name, nil
}

// ReadReadOnlyFile validates that path names a frozen retained file directly
// inside this store directory and returns its bounded contents.
func (s *Store) ReadReadOnlyFile(path string, limit int64) ([]byte, error) {
	if s == nil || path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("invalid retained frozen file path")
	}
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(s.directory)
	if filepath.Dir(cleanPath) != cleanDir {
		return nil, errors.New("retained frozen file is outside the admitted store directory")
	}
	name := filepath.Base(cleanPath)
	if !FrozenFileName(name) {
		return nil, errors.New("unrecognized retained frozen file identity")
	}
	return s.ReadRetained(name, limit)
}

// WriteJSONResource marshals value as JSON, stores it in the retained store,
// retains it in the thread catalog, and returns its record name.
func (s *Store) WriteJSONResource(ctx context.Context, thread string, limits Limits, writes *persistence.Counter, value any) (string, error) {
	if s == nil {
		return "", errors.New("retained storage is unavailable")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > MaxRecordBytes {
		return "", capacityError("retained JSON resource", int64(len(data)), MaxRecordBytes, "Reduce the retained resource before retrying.")
	}
	if limits.Total <= 0 {
		limits.Total = DefaultStorageBytes
	}
	name := fmt.Sprintf("resource-%x.json", sha256.Sum256(data))
	if err := s.Transaction(ctx, writes, func(tx *Transaction) error {
		if err := tx.Admit(limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.Write(name, "resource-pending-", data); err != nil {
			return err
		}
		return tx.Retain(thread, limits, name)
	}); err != nil {
		return "", err
	}
	return name, nil
}

// ReadJSONResource reads a previously published internal JSON resource record.
func (s *Store) ReadJSONResource(ref string) ([]byte, error) {
	if s == nil || !FrozenResourceName(ref) {
		return nil, errors.New("invalid retained JSON resource identity")
	}
	return s.ReadRetained(ref, MaxRecordBytes)
}
