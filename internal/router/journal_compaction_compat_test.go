package router

import (
	"bytes"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalCompactionRetainedCommandSegmentsAfterRestart(t *testing.T) {
	for _, explicitWorkspace := range []bool{true, false} {
		name := "explicit-workspace"
		if !explicitWorkspace {
			name = "omitted-workspace"
		}
		t.Run(name, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread, store := transform.shellThreadID, proxy.replayStore
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(transform.ctx, store, workspace, thread, "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Repair parser compatibility"), State: new("working")},
			}); err != nil {
				t.Fatal(err)
			}
			const report = "FAIL parser: retained evidence marker"
			hash := sha256.Sum256([]byte(report + "\ncomplete"))
			if hash == ([32]byte{}) {
				t.Fatal("fixture requires a nonzero output hash")
			}
			segments := &retainedCommandSegments{Command: "go test ./parser; echo complete", Exit: 0, Output: hash,
				Parts: []retainedCommandSegment{{Source: "go test ./parser", Exit: 1, Output: new(report)}, {Source: "echo complete", Output: new("complete")}}}
			id := commandSegmentsID("turn", "command")
			// Use the normal durable writer, not a hand-serialized fixture. Its
			// retained v1 numeric hash array must be readable by v2 compaction.
			if err := store.put(transform.ctx, workspace, map[string]mekugiHistory{
				id: {CommandSegments: segments},
				"failed-parser": {ExecutingThread: thread, Script: "go test ./parser", Report: report,
					ExecOutcome: &execOutcome{Status: execStatusFailed, Exit: new(1)}},
			}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(store.directory, replayRecordName(workspace, id, false)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(data, []byte(`"Output":[`)) {
				t.Fatalf("normal writer did not retain the numeric-array hash: %s", data)
			}
			var retained replayRecord
			if err := json.Unmarshal(data, &retained, jsonv1.FormatByteArrayAsArray(true)); err != nil {
				t.Fatalf("v2 reader rejected normal durable command segments: %v", err)
			}
			if retained.History.CommandSegments == nil || retained.History.CommandSegments.Output != hash || len(retained.History.CommandSegments.Parts) != 2 || retained.History.CommandSegments.Parts[0].Output == nil || *retained.History.CommandSegments.Parts[0].Output != report {
				t.Fatalf("retained command evidence changed: %+v", retained.History.CommandSegments)
			}
			reopened, err := openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = reopened
			request, headers := journalCompactionRequest(t, workspace, thread)
			if !explicitWorkspace {
				metadata, _ := decodeCodexTurnMetadata(headers)
				metadata.Directories = nil
				headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
			}
			provider := &serverFakeProvider{}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, headers, "compact-restarted", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 0 {
				t.Fatal("retained command segments forced provider compaction")
			}
			for _, fact := range []string{"Repair parser compatibility", "Failed: go test ./parser", "Exit: 1; output not retained", report, "response.completed"} {
				if !strings.Contains(output.String(), fact) {
					t.Errorf("local summary omitted durable fact %q: %s", fact, &output)
				}
			}
		})
	}
}
