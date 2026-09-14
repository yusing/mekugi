package router

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

const maxShellOutputBytes = 16 << 20
const maxShellOutputStoreBytes = 256 << 20

// Output records hold only the omitted suffixes, not executable state. Their
// random IDs are explicit read capabilities, portable through visible history.
type shellOutputRecord struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func shellOutputStore(manifest toolWorkerManifest) (*mekugiReplayStore, error) {
	if manifest.ReplayDirectory == "" {
		return nil, errors.New("output recovery storage is unavailable")
	}
	info, err := os.Lstat(filepath.Join(manifest.ReplayDirectory, "store.lock"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("output recovery storage is missing or invalid")
	}
	return &mekugiReplayStore{directory: manifest.ReplayDirectory}, nil
}

func validShellOutputID(id string) bool {
	value, ok := strings.CutPrefix(id, "ho_")
	decoded, err := hex.DecodeString(value)
	return ok && err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == value
}

func (s *mekugiReplayStore) putShellOutput(ctx context.Context, stdout, stderr string, exitCode int) (string, error) {
	if len(stdout)+len(stderr) > maxShellOutputBytes || !utf8.ValidString(stdout) || !utf8.ValidString(stderr) || exitCode < 0 || exitCode > 255 {
		return "", errors.New("omitted output exceeds 16 MiB, is not UTF-8, or has an invalid exit code")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := "ho_" + hex.EncodeToString(random[:])
	record := shellOutputRecord{Version: 1, ID: id, Stdout: stdout, Stderr: stderr, ExitCode: exitCode}
	data, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if len(data) > maxReplayRecordBytes {
		return "", errors.New("encoded output record exceeds recovery record limit")
	}
	err = s.locked(ctx, func() error {
		entries, err := os.ReadDir(s.directory)
		if err != nil {
			return err
		}
		total := int64(len(data))
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "output-") {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("invalid output recovery store entry")
			}
			total += info.Size()
		}
		if total > maxShellOutputStoreBytes {
			return errors.New("output recovery quota reached; explicit cleanup required")
		}
		return s.writeFile("output-"+id+".json", "output-pending-", data)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *mekugiReplayStore) readShellOutput(ctx context.Context, id string) (shellOutputRecord, error) {
	var record shellOutputRecord
	if !validShellOutputID(id) {
		return record, errors.New("invalid output ID")
	}
	err := s.readLocked(ctx, func() error {
		name := filepath.Join(s.directory, "output-"+id+".json")
		info, err := os.Lstat(name)
		if err != nil {
			return fmt.Errorf("output recovery record unavailable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
			return errors.New("invalid output recovery record file")
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, maxReplayRecordBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxReplayRecordBytes || !utf8.Valid(data) {
			return errors.New("invalid output recovery record encoding or size")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var required struct {
			Version  int     `json:"version"`
			ID       string  `json:"id"`
			Stdout   *string `json:"stdout"`
			Stderr   *string `json:"stderr"`
			ExitCode *int    `json:"exit_code"`
		}
		if err := decoder.Decode(&required); err != nil {
			return err
		}
		if required.Stdout == nil || required.Stderr == nil || required.ExitCode == nil {
			return errors.New("output recovery record is missing required fields")
		}
		record = shellOutputRecord{
			Version: required.Version, ID: required.ID, Stdout: *required.Stdout,
			Stderr: *required.Stderr, ExitCode: *required.ExitCode,
		}
		if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return errors.New("trailing output recovery data")
		}
		if record.Version != 1 || record.ID != id || record.ExitCode < 0 || record.ExitCode > 255 || len(record.Stdout)+len(record.Stderr) > maxShellOutputBytes {
			return errors.New("invalid output recovery record")
		}
		return nil
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
	id, err := store.putShellOutput(ctx, execution.OmittedOutput.Stdout, execution.OmittedOutput.Stderr, execution.ExitCode)
	if err != nil {
		return execution, err
	}
	execution.OmittedOutput = nil
	execution.Stderr += fmt.Sprintf("output truncated; read omitted remainder with houtput %s\n", id)
	return execution, nil
}
