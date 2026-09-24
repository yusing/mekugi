package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Failure records intentionally exclude messages, wrapped errors and request bodies.
type failureRecord struct {
	Version   int                 `json:"version"`
	Time      time.Time           `json:"time"`
	Thread    string              `json:"thread"`
	Phase     requestFailurePhase `json:"phase"`
	Code      string              `json:"code"`
	Reference string              `json:"reference"`
	Stream    jsontext.Value      `json:"stream,omitempty"`
}

func (c *CriticalErrors) persistFailure(f *requestFinalization) {
	if c == nil || f.diagnosticReference == "" {
		return
	}
	c.mu.Lock()
	store := c.failureStore
	enabled := c.persistFailures || store != nil
	c.mu.Unlock()
	if !enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if store == nil {
		directory, err := defaultMekugiReplayDirectory()
		if err == nil {
			store, err = openMekugiReplayStoreContext(ctx, directory)
		}
		if err == nil {
			c.mu.Lock()
			c.failureStore = store
			c.mu.Unlock()
		}
	}
	var err error
	if store == nil {
		err = errors.New("failure storage unavailable")
	} else {
		// Passthrough has no replay lifecycle; a failure-only store still needs
		// the shared, hourly-throttled age sweep as well as publication quotas.
		if sweepErr := store.cleanupSessions(ctx); sweepErr != nil {
			c.addNotice(f.sessionID, "failure_cleanup", "Mekugi could not complete failure-record retention cleanup.")
		}
		record := failureRecord{Version: 1, Time: time.Now().UTC(), Thread: f.threadID,
			Phase: f.failurePhase, Code: f.diagnosticCode, Reference: f.diagnosticReference}
		if f.streamDiagnostics != nil {
			record.Stream, err = json.Marshal(f.streamDiagnostics.snapshot())
		}
		if err == nil {
			err = store.appendFailure(ctx, record)
		}
	}
	if err != nil {
		c.addNotice(f.sessionID, "failure_storage", "Mekugi could not retain the failure reference. Relaunch with --debug to capture a future failure.")
	}
}

func (s *mekugiReplayStore) appendFailure(ctx context.Context, record failureRecord) error {
	copy := *s
	copy.session = storageSessionIdentity{Thread: record.Thread}
	if copy.session.Thread == "" {
		copy.session.Thread = "router-failures"
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("failure-%x.json", sha256.Sum256([]byte(rand.Text())))
	return copy.locked(ctx, func() error { return copy.writeManagedFile(name, "failure-pending-", data) })
}

func inspectFailures(ctx context.Context, directory, reference string, output io.Writer) error {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		entries, err = nil, nil
	}
	if err != nil {
		return err
	}
	records := []failureRecord{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), "failure-") || !retainedDataName(entry.Name()) {
			continue
		}
		data, err := readManagedOutputFile(filepath.Join(directory, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var record failureRecord
		if err := json.Unmarshal(data, &record); err != nil || record.Version != 1 || record.Time.IsZero() || record.Reference == "" {
			return errors.New("invalid retained failure record")
		}
		if reference == "" || reference == record.Reference {
			records = append(records, record)
		}
	}
	if reference != "" && len(records) == 0 {
		return fmt.Errorf("failure reference %q not found (it may have expired)", reference)
	}
	slices.SortFunc(records, func(a, b failureRecord) int { return a.Time.Compare(b.Time) })
	return json.MarshalEncode(jsontext.NewEncoder(output), records)
}
