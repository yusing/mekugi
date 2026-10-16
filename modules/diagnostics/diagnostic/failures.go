// Source: internal/router/failure_store.go@[ac8511fa] retained failure publication and lookup.
package diagnostic

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

var Failures = recordkind.Spec{ID: "failure", Match: func(name string) bool {
	if filepath.Base(name) != name {
		return false
	}
	hash, ok := strings.CutPrefix(name, "failure-")
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, ".json")
	if !ok || len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}}

// Failure records retain the actual error for post-session diagnosis.
type Failure struct {
	Version   int            `json:"version"`
	Time      time.Time      `json:"time"`
	Thread    string         `json:"thread"`
	Phase     string         `json:"phase"`
	Code      string         `json:"code"`
	Reference string         `json:"reference"`
	Error     string         `json:"error,omitempty"`
	Stream    jsontext.Value `json:"stream,omitempty"`
}

func (s *Service) OpenFailures(ctx context.Context, directory string) (*history.Store, error) {
	return s.history.Open(ctx, directory)
}
func (*Service) RetainFailure(ctx context.Context, store *history.Store, writes *persistence.Counter, limits history.Limits, record Failure) error {
	data, err := json.Marshal(&record)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("failure-%x.json", sha256.Sum256([]byte(rand.Text())))
	thread := record.Thread
	if thread == "" {
		thread = "router-failures"
	}
	return store.Transaction(ctx, writes, func(tx *history.Transaction) error {
		if err := tx.Retain(thread, limits, name); err != nil {
			return err
		}
		if err := tx.Admit(limits, name, int64(len(data))); err != nil {
			return err
		}
		return tx.Write(name, "failure-pending-", data)
	})
}
func (*Service) ReadFailures(ctx context.Context, directory, reference string) ([]Failure, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		entries, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := []Failure{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !Failures.Match(entry.Name()) {
			continue
		}
		data, err := history.ReadRecord(filepath.Join(directory, entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var record Failure
		if err := json.Unmarshal(data, &record); err != nil || record.Version != 1 || record.Time.IsZero() || record.Reference == "" {
			return nil, errors.New("invalid retained failure record")
		}
		if reference == "" || reference == record.Reference {
			records = append(records, record)
		}
	}
	if reference != "" && len(records) == 0 {
		return nil, fmt.Errorf("failure reference %q not found (it may have expired)", reference)
	}
	slices.SortFunc(records, func(a, b Failure) int { return a.Time.Compare(b.Time) })
	return records, nil
}
