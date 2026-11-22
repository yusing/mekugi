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

// LegacyOutputRecord and LegacyCursorRecord recognize retired pre-switch
// outputrecovery files at the storage lifecycle boundary only so upgraded
// stores continue expiring old records without blocking retention or restoring
// mread.
var LegacyOutputRecord = recordkind.Spec{ID: "output", Limit: 256 << 20, Match: LegacyOutputName}
var LegacyCursorRecord = recordkind.Spec{ID: "cursor", Match: LegacyCursorName}

var legacyOutputHashed = recordkind.HashedName("output-", ".json")

// LegacyCursorName recognizes pre-switch cursor-<64-hex>.json records.
var LegacyCursorName = recordkind.HashedName("cursor-", ".json")

// LegacyOutputName recognizes pre-switch output-<64-hex>.json and short-handle
// output-<handle>.json records using the retired owner's exact handle rule.
func LegacyOutputName(name string) bool {
	if legacyOutputHashed(name) {
		return true
	}
	id, ok := strings.CutPrefix(name, "output-")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, ".json")
	if !ok {
		return false
	}
	_, ok = ParseHandle(id)
	return ok
}

// LegacyRecoveryName reports whether name is a retired pre-switch
// outputrecovery record.
func LegacyRecoveryName(name string) bool {
	return LegacyOutputName(name) || LegacyCursorName(name)
}

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

func (s *Store) writeRetained(ctx context.Context, thread string, limits Limits, writes *persistence.Counter, resource, action, name, pattern string, data []byte, readOnly bool) error {
	if s == nil {
		return errors.New("retained storage is unavailable")
	}
	if strings.TrimSpace(thread) == "" {
		return fmt.Errorf("%s requires an owning thread", resource)
	}
	if len(data) > MaxRecordBytes {
		return capacityError(resource, int64(len(data)), MaxRecordBytes, action)
	}
	if limits.Total <= 0 {
		limits.Total = DefaultStorageBytes
	}
	return s.Transaction(ctx, writes, func(tx *Transaction) error {
		if err := tx.Retain(thread, limits, name); err != nil {
			return err
		}
		if err := tx.Admit(limits, name, int64(len(data))); err != nil {
			return err
		}
		if readOnly {
			return tx.WriteReadOnly(name, pattern, data)
		}
		return tx.Write(name, pattern, data)
	})
}

// WriteReadOnlyFile atomically publishes an immutable read-only (0o444) file in
// the store directory and retains its filename in the thread's session catalog.
func (s *Store) WriteReadOnlyFile(ctx context.Context, thread string, limits Limits, writes *persistence.Counter, kind, ext string, data []byte) (string, string, error) {
	if !validFrozenKind(kind) || (ext != ".txt" && ext != ".json") {
		return "", "", errors.New("invalid frozen file kind or extension")
	}
	name := fmt.Sprintf("frozen-%s-%x%s", kind, sha256.Sum256(data), ext)
	if err := s.writeRetained(ctx, thread, limits, writes, "retained frozen file", "Reduce the retained output before retrying.", name, "frozen-pending-", data, true); err != nil {
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
	if strings.TrimSpace(thread) == "" {
		return "", errors.New("retained JSON resource requires an owning thread")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("resource-%x.json", sha256.Sum256(data))
	if err := s.writeRetained(ctx, thread, limits, writes, "retained JSON resource", "Reduce the retained resource before retrying.", name, "resource-pending-", data, false); err != nil {
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
