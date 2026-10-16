// Source: internal/router/hook_observations.go:17:72@[3e523196] published hook history.
package history

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/recordkind"
)

// HookRun is the retained host-fact representation. Its field encoding preserves
// published records; the live Codex protocol is owned by the backend.
type HookRun struct {
	ID            string `json:"id"`
	EventName     string `json:"eventName"`
	HandlerType   string `json:"handlerType"`
	ExecutionMode string `json:"executionMode"`
	SourcePath    string `json:"sourcePath"`
	Status        string `json:"status"`
	StatusMessage string `json:"statusMessage"`
	StartedAt     int64  `json:"startedAt"` // Host epoch seconds, not milliseconds.
	CompletedAt   *int64 `json:"completedAt"`
	DurationMS    *int64 `json:"durationMs"`
	Entries       []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"entries"`
}

type HookObservation struct {
	Turn      string
	Run       HookRun
	At        time.Time
	Output    string
	HasError  bool
	Completed bool
}

type HookObservations struct {
	Version   int
	Workspace string
	Thread    string
	Dropped   int
	Runs      []HookObservation
	Bytes     int `json:"-"` // Published input size, charged to offline replay's corpus bound.
}

func HookObservationsName(workspace, thread string) string {
	return fmt.Sprintf("hooks-%x.json", sha256.Sum256([]byte(workspace+"\x00"+thread)))
}

// Offline readers use published files only, without opening a writable store
// or acquiring resources. The same reader runs under store.lock for publication.
func (s *Store) ReadHookObservations(workspace, thread string) (HookObservations, error) {
	r := HookObservations{Version: 1, Workspace: workspace, Thread: thread}
	if s == nil || s.directory == "" {
		return r, nil
	}
	path := filepath.Join(s.directory, HookObservationsName(workspace, thread))
	data, err := ReadRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r, json.RejectUnknownMembers(true)); err != nil {
		return r, err
	}
	r.Bytes = len(data)
	if r.Version != 1 || r.Workspace != workspace || r.Thread != thread {
		return r, errors.New("hook observation identity mismatch")
	}
	for i, run := range r.Runs {
		if run.Run.ID == "" || run.At.IsZero() {
			return r, errors.New("hook observation lacks identity or time")
		}
		// Earlier releases kept the status message as the first output line.
		// Host entries never use that label.
		if line, ok := strings.CutPrefix(run.Output, "status: "); ok && run.Run.StatusMessage == "" {
			r.Runs[i].Run.StatusMessage, r.Runs[i].Output, _ = strings.Cut(line, "\n")
		}
	}
	return r, nil
}

// HookRecord preserves the existing retained identity and shared store budget.
var HookRecord = recordkind.Spec{ID: "hooks", Match: func(name string) bool {
	if filepath.Base(name) != name {
		return false
	}
	hash, ok := strings.CutPrefix(name, "hooks-")
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
