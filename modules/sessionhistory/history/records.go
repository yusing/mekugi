package history

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/persistence"
)

// MaxRecordBytes is the retained wire-record bound.
const MaxRecordBytes = 32 << 20

// ReadRetained borrows this store's admitted directory for a bounded record read.
// Binary feature records need not be UTF-8; their owner validates the payload.
func (s *Store) ReadRetained(name string, limit int64) ([]byte, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name || limit < 0 || limit > MaxRecordBytes {
		return nil, errors.New("invalid retained record read")
	}
	file, err := persistence.OpenRetained(filepath.Join(s.directory, name), int(limit))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("retained record exceeds read bound")
	}
	return data, err
}

// ReadRecord reads one regular, bounded UTF-8 retained record without effects.
// On a read or validation failure, returned bytes are for resource accounting
// only; they are not a valid retained record.
// Source: internal/router/shell_output_store.go:304:331@efcbf71f readManagedOutputFile.
func ReadRecord(name string) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxRecordBytes {
		return nil, errors.New("invalid read recovery record file")
	}
	file, err := persistence.OpenRetained(name, MaxRecordBytes)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// Managed records are atomically replaced, so their stat size usually
	// matches the complete read. Reserve one read's spare capacity to avoid
	// geometric io.ReadAll growth, while retaining the bound if size changes.
	var buffer bytes.Buffer
	buffer.Grow(int(info.Size()) + bytes.MinRead)
	_, err = buffer.ReadFrom(io.LimitReader(file, MaxRecordBytes+1))
	data := buffer.Bytes()
	if err != nil {
		return data, err
	}
	if len(data) > MaxRecordBytes || !utf8.Valid(data) {
		return data, errors.New("invalid read recovery record encoding or size")
	}
	return data, nil
}
