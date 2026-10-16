// Source: internal/router/debug_ax.go:23:127@[ac8511fa] bounded AX export.
package diagnostic

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type debugAXThread struct {
	Reads    jsontext.Value `json:"reads,omitempty"`
	ThreadID string         `json:"thread_id"`
	State    string         `json:"state"`
	Report   jsontext.Value `json:"report,omitempty"`
}

func (d *Bundle) ObserveThread(id string) {
	if d == nil || d.service.sections.ReadSnapshot == nil || id == "" {
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
func (d *Bundle) writeAXReport() error {
	if d.service.sections.ReadSnapshot == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log, logErr := d.service.sections.ReadSnapshot(ctx, d.axReadPath)
	threads := slices.Sorted(maps.Keys(d.axThreads))
	var logOnly []string
	for _, id := range log.Threads() {
		if id != "" && !d.axThreads[id] {
			logOnly = append(logOnly, id)
		}
	}
	slices.Sort(logOnly)
	dropped := len(logOnly) > 256
	logOnly = logOnly[:min(len(logOnly), 256)]
	paths, discoveryErr := d.discover(ctx, append(slices.Clone(threads), logOnly...))
	report := struct {
		Schema            string          `json:"schema"`
		Scope             string          `json:"scope"`
		ReadLog           string          `json:"read_log"`
		LogState          string          `json:"log_state"`
		DroppedThreads    bool            `json:"dropped_threads"`
		Threads           []debugAXThread `json:"threads"`
		LogOnlyThreads    []debugAXThread `json:"log_only_threads"`
		DroppedLogThreads bool            `json:"dropped_log_threads"`
		UnattributedReads jsontext.Value  `json:"unattributed_reads,omitempty"`
	}{
		Schema: "mekugi.ax.debug.v2", Scope: "known router threads and separately labeled log-only evidence; whole-rollout evidence at shutdown",
		ReadLog: d.axReadPath, LogState: "observed", DroppedThreads: d.axDroppedThreads,
		Threads: []debugAXThread{}, LogOnlyThreads: []debugAXThread{}, DroppedLogThreads: dropped,
	}
	if logErr != nil {
		report.LogState = "invalid_or_unavailable"
	}
	anonymous, err := log.Anonymous()
	if err != nil {
		return err
	}
	report.UnattributedReads = anonymous
	inspect := func(id string) debugAXThread {
		entry := debugAXThread{ThreadID: id, State: "rollout_unavailable"}
		reads, collector, snapshotErr := log.Select(id)
		if discoveryErr != nil {
			entry.State = "discovery_incomplete"
		} else if len(paths[id]) > 1 {
			entry.State = "rollout_ambiguous"
		} else if len(paths[id]) == 1 {
			err := snapshotErr
			var result query.Result
			if err == nil {
				result, err = d.service.inspection.Inspect(ctx, query.SingleOptions{Session: paths[id][0], ReplayDirectory: d.replayDirectory, AX: true, Limit: 1, TextBytes: 4096, Collector: collector})
			}
			if err != nil || len(result.AX) == 0 {
				entry.State = "inspection_failed"
			} else if result.ThreadID != id {
				entry.State = "rollout_identity_mismatch"
			} else {
				entry.State, entry.Report = "observed", jsontext.Value(result.AX)
			}

		}
		if entry.Report == nil {
			entry.Reads = reads
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
	return errors.Join(json.MarshalEncode(jsontext.NewEncoder(d.writes.Writer(file)), &report, jsonv1.DefaultOptionsV1()), file.Close())
}
