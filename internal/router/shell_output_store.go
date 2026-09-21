package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

const maxShellOutputBytes = 16 << 20
const maxShellOutputStoreBytes = 256 << 20

// Initial references own immutable omitted data. Later references contain only
// a source reference and positions, never another copy of the output.
type shellOutputRecord struct {
	Changes      *changeReadSnapshot `json:"changes,omitempty"`
	Version      int                 `json:"version"`
	ID           string              `json:"id"`
	Stdout       string              `json:"stdout"`
	Stderr       string              `json:"stderr"`
	ExitCode     int                 `json:"exit_code"`
	StdoutKind   string              `json:"stdout_kind,omitempty"`
	StderrKind   string              `json:"stderr_kind,omitempty"`
	Source       string              `json:"source,omitempty"`
	Position     [2]int              `json:"position,omitempty"`
	Stream       string              `json:"stream,omitempty"`
	CursorDigest string              `json:"cursor_digest,omitempty"`
	Binding      string              `json:"binding,omitempty"`
}

type changeReadSnapshot struct {
	Workspace string   `json:"workspace"`
	IDs       []string `json:"ids"`
	Paths     []string `json:"paths"`
	View      string   `json:"view"`
	Offset    int      `json:"offset"`
	Digest    string   `json:"digest"`
}

func (s *mekugiReplayStore) putChangeRead(ctx context.Context, options changeReadOptions, text string, offset int) (string, error) {
	handles, err := s.allocateHandles(ctx, 1)
	if err != nil {
		return "", err
	}
	return s.putReadRecord(ctx, shellOutputRecord{
		Version: 1, ID: handles[0],
		Changes: &changeReadSnapshot{
			Workspace: options.workspace, IDs: options.ids, Paths: options.paths, View: options.view,
			Offset: offset, Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(text))),
		},
	})
}

func (s *mekugiReplayStore) readSourceStreams(ctx context.Context, record shellOutputRecord) (toolplugin.OmittedOutput, error) {
	output := toolplugin.OmittedOutput{
		Stdout: record.Stdout, Stderr: record.Stderr, StdoutKind: record.StdoutKind, StderrKind: record.StderrKind,
	}
	if record.Changes == nil {
		return output, nil
	}
	selection := record.Changes
	text, err := s.readChanges(ctx, changeReadOptions{
		workspace: selection.Workspace, ids: selection.IDs, paths: selection.Paths, view: selection.View,
	})
	if err != nil {
		return output, err
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(text))) != selection.Digest ||
		selection.Offset < 0 || selection.Offset >= len(text) || !utf8.RuneStart(text[selection.Offset]) {
		return output, errors.New("change snapshot changed; repeat the original hchanges read")
	}
	output.Stdout = text[selection.Offset:]
	return output, nil
}

func shellOutputStore(manifest toolWorkerManifest) (*mekugiReplayStore, error) {
	if manifest.ReplayDirectory == "" {
		return nil, errors.New("read recovery storage is unavailable")
	}
	info, err := os.Lstat(filepath.Join(manifest.ReplayDirectory, "store.lock"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("read recovery storage is missing or invalid")
	}
	return &mekugiReplayStore{directory: manifest.ReplayDirectory, maxBytes: defaultReplayStorageBytes, maxCommentaryBytes: 16 << 20, storageNotice: func(_ string, message string) { _, _ = fmt.Fprintln(os.Stderr, message) }}, nil
}

func validShellOutputID(id string) bool {
	_, ok := parseShortHandle(id)
	return ok
}

func readRecordBinding(record shellOutputRecord) string {
	data, _ := json.Marshal(record)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func validReadKind(kind string) bool {
	return kind == "" || kind == "rows" || kind == "json"
}

func (s *mekugiReplayStore) putShellOutput(ctx context.Context, stdout, stderr string, exitCode int) (string, error) {
	return s.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: stdout, Stderr: stderr}, exitCode)
}

func (s *mekugiReplayStore) putTypedOutput(ctx context.Context, output toolplugin.OmittedOutput, exitCode int) (string, error) {
	handles, err := s.allocateHandles(ctx, 1)
	if err != nil {
		return "", err
	}
	record := shellOutputRecord{
		Version: 1, ID: handles[0],
		Stdout: output.Stdout, Stderr: output.Stderr, ExitCode: exitCode,
		StdoutKind: output.StdoutKind, StderrKind: output.StderrKind,
	}
	return s.putReadRecord(ctx, record)
}

func (s *mekugiReplayStore) putReadCursor(ctx context.Context, source shellOutputRecord, position [2]int, stream string) (string, error) {
	record := shellOutputRecord{
		Version: 1, Source: source.ID, Position: position, Stream: stream,
		Binding: readRecordBinding(source),
	}
	record.CursorDigest = readCursorDigest(record)
	s = s.scoped(ctx)
	err := s.locked(ctx, func() error {
		scope, _, err := s.readHandleScope(s.handleNamespace())
		if err != nil {
			return err
		}
		candidates := append(slices.Clone(scope.Inherited), handleRange{Namespace: scope.Namespace, End: scope.Next})
		for _, candidate := range candidates {
			name := scopedCursorName(candidate.Namespace, record.CursorDigest)
			data, err := readManagedOutputFile(filepath.Join(s.directory, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &record.ID); err != nil {
				return err
			}
			number, ok := parseShortHandle(record.ID)
			if !ok {
				return errors.New("invalid read cursor handle")
			}
			if number >= candidate.End {
				continue // This cursor was allocated after the fork snapshot.
			}
			owner, err := s.handleOwner(record.ID)
			if err != nil || owner != candidate.Namespace {
				return errors.New("read cursor scope mismatch")
			}
			return s.retainFiles(name)
		}
		handles, err := s.allocateHandlesLocked(1)
		if err != nil {
			return err
		}
		record.ID = handles[0]
		name := scopedCursorName(s.handleNamespace(), record.CursorDigest)
		return s.writeManagedFile(name, "cursor-pending-", mustMarshalJSON(record.ID))
	})
	if err != nil {
		return "", err
	}
	return s.putReadRecord(ctx, record)
}

func readCursorDigest(record shellOutputRecord) string {
	record.ID, record.CursorDigest = "", ""
	data, _ := json.Marshal(record)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func validateReadRecord(record shellOutputRecord) error {
	if size := len(record.Stdout) + len(record.Stderr); size > maxShellOutputBytes {
		return storageCapacityError("saved read output", int64(size), maxShellOutputBytes, "Narrow the command output or split the operation before retrying.")
	}
	if record.Version != 1 || !validShellOutputID(record.ID) ||
		record.ExitCode < 0 || record.ExitCode > 255 ||
		!utf8.ValidString(record.Stdout) || !utf8.ValidString(record.Stderr) ||
		!validReadKind(record.StdoutKind) || !validReadKind(record.StderrKind) {
		return errors.New("invalid read recovery record")
	}
	if record.Changes != nil {
		selection := record.Changes
		if record.Source != "" || record.Stdout != "" || record.Stderr != "" ||
			record.StdoutKind != "" || record.StderrKind != "" ||
			selection.Offset < 0 || len(selection.Digest) != 64 || len(selection.IDs) == 0 ||
			(selection.View != "" && selection.View != "summary" && selection.View != "history") {
			return errors.New("invalid change read selection")
		}
	}
	if record.Stream != "" && record.Stream != "stdout" && record.Stream != "stderr" {
		return errors.New("invalid read stream selection")
	}
	if record.Source != "" {
		if !validShellOutputID(record.Source) || record.Source == record.ID ||
			record.Stdout != "" || record.Stderr != "" || record.StdoutKind != "" || record.StderrKind != "" ||
			record.Position[0] < 0 || record.Position[1] < 0 || len(record.Binding) != 64 || record.CursorDigest != readCursorDigest(record) {
			return errors.New("invalid read continuation record")
		}
	} else if record.Position != [2]int{} || record.Stream != "" || record.Binding != "" || record.CursorDigest != "" {
		return errors.New("invalid initial read reference")
	}
	for index, text := range []string{record.Stdout, record.Stderr} {
		kind := []string{record.StdoutKind, record.StderrKind}[index]
		if kind == "rows" && text != "" && !strings.HasSuffix(text, "\n") {
			return errors.New("verified rows must have complete line endings")
		}
		if kind == "json" {
			var entries []json.RawMessage
			if json.Unmarshal([]byte(text), &entries) != nil || entries == nil {
				return errors.New("structured read output must be a JSON array")
			}
		}
	}
	return nil
}

func (s *mekugiReplayStore) putReadRecord(ctx context.Context, record shellOutputRecord) (string, error) {
	s = s.scoped(ctx)
	if err := validateReadRecord(record); err != nil {
		return "", err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if len(data) > maxReplayRecordBytes {
		return "", storageCapacityError("encoded read record", int64(len(data)), maxReplayRecordBytes, "Narrow the command output or split the operation before retrying.")
	}
	err = s.locked(ctx, func() error {
		name, err := s.outputName(record.ID)
		if err != nil {
			return err
		}
		if previous, err := readManagedOutputFile(filepath.Join(s.directory, name)); err == nil {
			if !bytes.Equal(previous, data) {
				return errors.New("read reference conflicts with retained record")
			}
			if err := s.retainReadRecord(record); err != nil {
				return err
			}
			return syncReplayDirectory(s.directory)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return s.writeManagedFile(name, "output-pending-", data)
	})
	if err != nil {
		return "", err
	}
	return record.ID, nil
}

func readManagedOutputFile(name string) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
		return nil, errors.New("invalid read recovery record file")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReplayRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReplayRecordBytes || !utf8.Valid(data) {
		return nil, errors.New("invalid read recovery record encoding or size")
	}
	return data, nil
}

func (s *mekugiReplayStore) readShellOutput(ctx context.Context, id string) (shellOutputRecord, error) {
	var record shellOutputRecord
	if !validShellOutputID(id) {
		return record, errors.New("invalid read reference")
	}
	s = s.scoped(ctx)
	err := s.locked(ctx, func() error {
		name, err := s.outputName(id)
		if err != nil {
			return err
		}
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read reference %s is unavailable; session data may have been cleaned after 14 days of inactivity or storage pressure", id)
		}
		if err != nil {
			return err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		required := struct {
			*shellOutputRecord
			Stdout   *string `json:"stdout"`
			Stderr   *string `json:"stderr"`
			ExitCode *int    `json:"exit_code"`
		}{shellOutputRecord: &record}
		if err := decoder.Decode(&required); err != nil {
			return err
		}
		if required.Stdout == nil || required.Stderr == nil || required.ExitCode == nil {
			return errors.New("read recovery record is missing required fields")
		}
		record.Stdout, record.Stderr, record.ExitCode = *required.Stdout, *required.Stderr, *required.ExitCode
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return errors.New("trailing read recovery data")
		}
		if record.ID != id {
			return errors.New("read recovery identity mismatch")
		}
		if err := validateReadRecord(record); err != nil {
			return err
		}
		return s.retainReadRecord(record)
	})
	return record, err
}

func retainExecutionOutput(ctx context.Context, manifest toolWorkerManifest, execution toolplugin.ExecutionOutput) (toolplugin.ExecutionOutput, error) {
	if execution.OmittedOutput == nil {
		return execution, nil
	}
	store, err := shellOutputStore(manifest)
	if err != nil {
		return execution, err
	}
	id, err := store.putTypedOutput(ctx, *execution.OmittedOutput, execution.ExitCode)
	if err != nil {
		return execution, err
	}
	execution.OmittedOutput = nil
	execution.Stderr += readNextCall(id)
	return execution, nil
}

func readNextCall(id string) string {
	return fmt.Sprintf("read: incomplete; next_call: mread %s\n", id)
}
