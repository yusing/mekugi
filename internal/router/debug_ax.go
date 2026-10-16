package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/modules/sessioninspection/query"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
)

type debugAXThread struct {
	Reads    *evidence.AXReadMetrics `json:"reads,omitempty"`
	ThreadID string                  `json:"thread_id"`
	State    string                  `json:"state"`
	Report   json.RawMessage         `json:"report,omitempty"`
}

func (d *debugOutput) observeAXThread(id string) {
	if d == nil || d.agentMetrics == nil || id == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.axThreads) == 256 && !d.axThreads[id] {
		d.axDroppedThreads = true
		return
	}
	d.axThreads[id] = true
}

// Called under the debug output lock after request shutdown. Log-only
// identities remain separate from known router threads; they are not presumed
// to be children or tests. Every supplied event is validated, including exclusions.
func (d *debugOutput) writeAXReport() error {
	if d.agentMetrics == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log, logErr := d.agentMetrics.ReadLog(ctx, d.axReadPath)
	threads := slices.Sorted(maps.Keys(d.axThreads))
	var logOnly []string
	for id := range log.Threads {
		if id != "" && !d.axThreads[id] {
			logOnly = append(logOnly, id)
		}
	}
	slices.Sort(logOnly)
	dropped := len(logOnly) > 256
	logOnly = logOnly[:min(len(logOnly), 256)]
	paths, discoveryErr := discoverDebugRollouts(ctx, append(slices.Clone(threads), logOnly...))
	report := struct {
		Schema            string                  `json:"schema"`
		Scope             string                  `json:"scope"`
		ReadLog           string                  `json:"read_log"`
		LogState          string                  `json:"log_state"`
		DroppedThreads    bool                    `json:"dropped_threads"`
		Threads           []debugAXThread         `json:"threads"`
		LogOnlyThreads    []debugAXThread         `json:"log_only_threads"`
		DroppedLogThreads bool                    `json:"dropped_log_threads"`
		UnattributedReads *evidence.AXReadMetrics `json:"unattributed_reads,omitempty"`
	}{
		Schema: "mekugi.ax.debug.v2", Scope: "known router threads and separately labeled log-only evidence; whole-rollout evidence at shutdown",
		ReadLog: d.axReadPath, LogState: "observed", DroppedThreads: d.axDroppedThreads,
		Threads: []debugAXThread{}, LogOnlyThreads: []debugAXThread{}, DroppedLogThreads: dropped,
	}
	if logErr != nil {
		report.LogState = "invalid_or_unavailable"
	}
	if reads, ok := log.Threads[""]; ok {
		report.UnattributedReads = &reads
	}
	inspect := func(id string) debugAXThread {
		entry := debugAXThread{ThreadID: id, State: "rollout_unavailable"}
		reads := log.ForThread(id)
		if logErr != nil {
			reads.State = "invalid_or_unavailable"
		}
		if discoveryErr != nil {
			entry.State = "discovery_incomplete"
		} else if len(paths[id]) > 1 {
			entry.State = "rollout_ambiguous"
		} else if len(paths[id]) == 1 {
			replayDirectory, err := defaultMekugiReplayDirectory()
			var result query.Result
			if err == nil {
				result, err = d.inspection.Inspect(ctx, query.SingleOptions{Session: paths[id][0], ReplayDirectory: replayDirectory, AX: true, Limit: 1, TextBytes: 4096, Collector: d.agentMetrics.NewInspection(&reads)})
			}
			if err != nil || len(result.AX) == 0 {
				entry.State = "inspection_failed"
			} else if result.ThreadID != id {
				entry.State = "rollout_identity_mismatch"
			} else {
				entry.State, entry.Report = "observed", json.RawMessage(result.AX)
			}

		}
		if entry.Report == nil {
			entry.Reads = &reads
		}
		return entry
	}
	for _, id := range threads {
		report.Threads = append(report.Threads, inspect(id))
	}
	for _, id := range logOnly {
		report.LogOnlyThreads = append(report.LogOnlyThreads, inspect(id))
	}
	file, err := os.OpenFile(d.axReportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return errors.Join(json.NewEncoder(d.writes.Writer(file)).Encode(report), file.Close())
}

func discoverDebugRollouts(ctx context.Context, threads []string) (map[string][]string, error) {
	found := make(map[string][]string)
	if len(threads) == 0 {
		return found, nil
	}
	codexDirectory := os.Getenv("CODEX_HOME")
	if codexDirectory == "" {
		userDirectory, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		codexDirectory = filepath.Join(userDirectory, ".codex")
	}
	visited := 0
	for _, name := range []string{"sessions", "archived_sessions"} {
		directory := filepath.Join(codexDirectory, name)
		err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				if path == directory && errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			visited++
			if visited > 100000 {
				return errors.New("rollout discovery exceeds entry bound")
			}
			if entry.Type().IsRegular() && slices.ContainsFunc(threads, func(id string) bool {
				return strings.HasSuffix(entry.Name(), "-"+id+".jsonl")
			}) {
				// Filenames only select candidates: "other-thread" also ends
				// in "thread". Attribute by the rollout's exact metadata ID.
				id, err := debugRolloutIdentity(path)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if slices.Contains(threads, id) {
					found[id] = append(found[id], path)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return found, nil
}

// Read only the bounded metadata header during discovery. Full rollout validation
// remains with the inspector; unreadable candidates cannot certify uniqueness.
func debugRolloutIdentity(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSessionInspectionBytes {
		return "", errors.New("rollout must be a bounded regular file")
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maxReplayRecordBytes+1))
	scanner.Buffer(make([]byte, 4096), maxReplayRecordBytes)
	var metadata struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &metadata) != nil ||
		metadata.Type != "session_meta" || metadata.Payload.ID == "" {
		return "", errors.New("rollout metadata is unavailable or invalid")
	}
	return metadata.Payload.ID, nil
}
