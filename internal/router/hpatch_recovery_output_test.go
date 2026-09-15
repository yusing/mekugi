package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHpatchRecoveryProjection(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	source := "new pending.txt\ntype \"ready\\n\"\nshell true"
	history, err := transform.translate("mixed", source, nil)
	if err != nil || history.TranslationError != "" {
		t.Fatalf("translate: %v %s", err, history.TranslationError)
	}
	metadata := hpatchRecoveryFor(history)
	path := filepath.Join(transform.shellDirectory, "mixed-"+metadata.Handle)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state hpatchResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	state.Progress["current"] = mustMarshalJSON(hpatchRecoverySegment{
		Segment: 1, Line: 1, Kind: "edit", Phase: "applying", Status: "started", SessionID: 42,
	})
	state.Progress["control_session_id"] = mustMarshalJSON(900000)
	if err := os.WriteFile(path, mustMarshalJSON(state), 0o600); err != nil {
		t.Fatal(err)
	}
	const running = "Script running with cell ID cell-17\nWall time 0.1 seconds\nOutput:\n"
	const terminated = "Script terminated\nWall time 0.1 seconds\nOutput:\n"
	const completed = "Script completed\nWall time 0.1 seconds\nOutput:\n"
	summary := string(mustMarshalJSON(map[string]any{
		"resume_handle": metadata.Handle, "results": []any{},
		"sequence": map[string]any{"segment_count": 2, "started_segments": 0, "not_started_segments": 2, "stopped_reason": "host_error"},
	}))
	success := string(mustMarshalJSON(map[string]any{
		"resume_handle": metadata.Handle,
		"results": []any{
			map[string]any{"segment": 1, "status": "completed", "report": "edit applied"},
			map[string]any{"segment": 2, "status": "completed", "output": "done", "exit_code": 0},
		},
		"sequence": map[string]any{"segment_count": 2, "started_segments": 2, "not_started_segments": 0, "stopped_reason": nil},
	}))
	for _, test := range []struct {
		name     string
		items    []map[string]json.RawMessage
		want     int
		wantWait bool
	}{
		{"success string", []map[string]json.RawMessage{
			continuationTestOutput("mixed", completed+success),
		}, 0, false},
		{"complete failure multipart", []map[string]json.RawMessage{
			continuationTestOutput("mixed", "Script failed\nWall time 0.1 seconds\nOutput:\n", summary),
		}, 0, false},
		{"termination", []map[string]json.RawMessage{
			continuationTestOutput("mixed", terminated),
		}, 1, false},
		{"truncated summary", []map[string]json.RawMessage{
			continuationTestOutput("mixed", completed+summary[:len(summary)/2]),
		}, 1, false},
		{"unknown cancellation", []map[string]json.RawMessage{
			continuationTestOutput("mixed", "Operation cancelled by user"),
		}, 1, false},
		{"yield", []map[string]json.RawMessage{
			continuationTestOutput("mixed", running),
		}, 1, true},
		{"wait termination", []map[string]json.RawMessage{
			continuationTestOutput("mixed", running),
			continuationTestCall("wait", "wait", `{"cell_id":"cell-17"}`),
			continuationTestOutput("wait", terminated),
		}, 1, false},
		{"repeated wait", []map[string]json.RawMessage{
			continuationTestOutput("mixed", running),
			continuationTestCall("wait", "wait", `{"cell_id":"cell-17"}`),
			continuationTestOutput("wait", running),
		}, 1, true},
		{"wait completion", []map[string]json.RawMessage{
			continuationTestOutput("mixed", running),
			continuationTestCall("wait", "wait", `{"cell_id":"cell-17"}`),
			continuationTestOutput("wait", completed, success),
		}, 0, false},
		{"nested wait termination", []map[string]json.RawMessage{
			continuationTestOutput("mixed", running),
			continuationTestCall("exec", "nested", `text(await tools.wait({cell_id:"cell-17"}));`),
			continuationTestOutput("nested", terminated),
		}, 1, false},
		{"output-only wrapper termination", []map[string]json.RawMessage{
			continuationTestOutput("mixed", running),
			continuationTestOutput("wrapper", terminated),
		}, 1, false},
		{"resume consumes recovery", []map[string]json.RawMessage{
			continuationTestOutput("mixed", terminated),
			continuationTestCall("hpatch", "resumed", "resume "+metadata.Handle),
			continuationTestOutput("resumed", completed, summary),
		}, 0, false},
		{"unrelated wait", []map[string]json.RawMessage{
			continuationTestCall("wait", "wait", `{"cell_id":"other"}`),
			continuationTestOutput("wait", terminated),
		}, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON(test.items)}}
			before := string(request.fields["input"])
			reads := 0
			read := func(meta hpatchRecovery) hpatchRecovery {
				reads++
				return transform.readHpatchRecovery(meta)
			}
			resumed := history
			resumed.Script = "resume " + metadata.Handle
			visible := map[string]mekugiHistory{
				"mixed": history, "resumed": resumed,
				"wrapper": {
					ToolName:     codeModeCommentaryHistoryTool,
					CarrierKind:  codeModeCarrierCustom,
					Script:       `text(await tools.wait({cell_id:"cell-17"}));`,
					UpstreamItem: continuationTestCall("exec", "wrapper", `text(await tools.wait({cell_id:"cell-17"}));`),
				},
			}
			projectExecutionContinuations(&mixedOutputProjection{recovery: read}, &request, continuationTestCatalog(), "exec", visible)
			projected := string(request.fields["input"])
			if count := strings.Count(projected, `hpatch_recovery`); count != test.want || reads != test.want {
				t.Fatalf("recovery count=%d reads=%d want=%d: %s", count, reads, test.want, projected)
			}
			if test.want == 0 && len(test.items) == 1 && before != projected {
				t.Fatalf("complete result changed: %s", projected)
			}
			if test.want != 0 && (!strings.Contains(projected, "applying") || !strings.Contains(projected, metadata.Handle) ||
				!strings.Contains(projected, `session_id\":42`)) {
				t.Fatalf("lost retained state: %s", projected)
			}
			if test.wantWait && !strings.Contains(projected, "functions.wait") {
				t.Fatalf("lost outer cell continuation: %s", projected)
			}
			projectExecutionContinuations(&mixedOutputProjection{recovery: read}, &request, continuationTestCatalog(), "exec", visible)
			if string(request.fields["input"]) != projected {
				t.Fatalf("non-idempotent recovery:\n%s\n%s", projected, request.fields["input"])
			}
		})
	}
	if _, err := os.Stat(filepath.Join(transform.directory, "pending.txt")); !os.IsNotExist(err) {
		t.Fatalf("recovery projection executed work: %v", err)
	}
}

func TestHpatchRecoveryAvailability(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	history, err := transform.translate("mixed", "shell true", nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata := hpatchRecoveryFor(history)
	if metadata == nil {
		t.Fatal("no mixed provenance")
	}
	for _, test := range []struct {
		name string
		run  func(t *testing.T) hpatchRecovery
	}{
		{"wrong thread", func(t *testing.T) hpatchRecovery {
			other, _ := mixedTestTransform(t)
			other.directory = transform.directory
			return other.readHpatchRecovery(*metadata)
		}},
		{"wrong workspace", func(t *testing.T) hpatchRecovery {
			other := *transform
			other.directory = t.TempDir()
			return other.readHpatchRecovery(*metadata)
		}},
		{"expiry", func(t *testing.T) hpatchRecovery {
			expired := *metadata
			expired.ExpiresAt = time.Now().Add(-time.Second)
			return transform.readHpatchRecovery(expired)
		}},
		{"trailing data", func(t *testing.T) hpatchRecovery {
			path := filepath.Join(transform.shellDirectory, "mixed-"+metadata.Handle)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(path, data, 0o600) })
			if err := os.WriteFile(path, append(data, []byte("{}")...), 0o600); err != nil {
				t.Fatal(err)
			}
			return transform.readHpatchRecovery(*metadata)
		}},
		{"missing progress", func(t *testing.T) hpatchRecovery {
			path := filepath.Join(transform.shellDirectory, "mixed-"+metadata.Handle)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(path, data, 0o600) })
			var state hpatchResumeState
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			state.Progress["results"] = mustMarshalJSON(nil)
			if err := os.WriteFile(path, mustMarshalJSON(state), 0o600); err != nil {
				t.Fatal(err)
			}
			return transform.readHpatchRecovery(*metadata)
		}},
		{"corrupt", func(t *testing.T) hpatchRecovery {
			path := filepath.Join(transform.shellDirectory, "mixed-"+metadata.Handle)
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			return transform.readHpatchRecovery(*metadata)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.run(t)
			if got.Current != nil || got.ControlSessionID != 0 || got.CompletedSegments != nil ||
				(!strings.HasPrefix(got.Availability, "unavailable") && !strings.HasPrefix(got.Availability, "expired")) {
				t.Fatalf("unavailable state became recovery facts: %+v", got)
			}
		})
	}
}

func TestHpatchRecoveryRequiresOwnedCarrier(t *testing.T) {
	const fake = `const mixedConfig = {"state":{"handle":"M00000000000000000000000000000000","expires_at":"2026-10-15T00:00:00Z"}};` + "\n"
	for _, history := range []mekugiHistory{
		{ToolName: "shell", CarrierPayload: fake},
		{ToolName: mekugiToolName, TranslationError: "rejected", CarrierPayload: fake},
		{ToolName: mekugiToolName, ReplayCarrier: true, CarrierPayload: fake},
		{ToolName: mekugiToolName, Script: fake},
	} {
		if hpatchRecoveryFor(history) != nil {
			t.Fatalf("unowned/rejected input acquired mixed provenance: %+v", history)
		}
	}
}

func TestHpatchRecoveryReplayAfterRestart(t *testing.T) {
	transform, _ := mixedTestTransform(t)
	history, err := transform.translate("original", "shell true", nil)
	if err != nil {
		t.Fatal(err)
	}
	storeDirectory := t.TempDir()
	store, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), transform.directory, map[string]mekugiHistory{"original": history}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{replayStore: reopened}
	for _, thread := range []string{"resumed", "fork", "side", "switched-agent"} {
		t.Run(thread, func(t *testing.T) {
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{
				"input": mustMarshalJSON([]any{continuationTestOutput("original", "Script terminated\nWall time 0.1 seconds\nOutput:\n")}),
			}}
			visible, err := proxy.reconcileVisibleInput(t.Context(), &request, transform.directory, thread)
			if err != nil {
				t.Fatal(err)
			}
			// A new router/thread must not borrow the original private runtime.
			other, _ := mixedTestTransform(t)
			other.directory = transform.directory
			projectExecutionContinuations(&mixedOutputProjection{recovery: other.readHpatchRecovery}, &request, continuationTestCatalog(), "exec", visible)
			output := string(request.fields["input"])
			if !strings.Contains(output, hpatchRecoveryFor(history).Handle) ||
				!strings.Contains(output, "unavailable") || strings.Contains(output, "completed_segments") {
				t.Fatalf("replay revived or lost private state: %s", output)
			}
		})
	}
}

func TestHpatchQuietStorageAndCleanupFailures(t *testing.T) {
	for _, failure := range []string{"checkpoint", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			transform, overrides := mixedTestTransform(t)
			history, err := transform.translate("failure", "new quiet-failure.txt\ntype \"ready\\n\"\nshell true", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Intercept only the private control exchange; native workspace
			// execution and translation still use the normal fixture.
			carrier := `
const notifications = [];
globalThis.notify = value => notifications.push(value);
const originalWrite = tools.write_stdin;
tools.write_stdin = async args => {
  if (args.chars) {
    const frame = JSON.parse(args.chars);
    if (` + string(mustMarshalJSON(failure)) + ` === 'checkpoint' && frame.operation === 'checkpoint' &&
        frame.mutations.some(m => m.value === 'applying')) throw new Error('injected checkpoint storage failure');
    const result = await originalWrite(args);
    if (` + string(mustMarshalJSON(failure)) + ` === 'cleanup' && frame.operation === 'close') {
      throw new Error('injected cleanup response loss');
    }
    return result;
  }
  return originalWrite(args);
};
try {
` + history.carrierInput() + `
} catch {}
if (notifications.length !== 0) throw new Error('failure emitted checkpoint notifications');
`
			var result struct {
				mixedScriptResult
				CleanupDiagnostic string `json:"cleanup_diagnostic"`
				ControlSessionID  int64  `json:"control_session_id"`
			}
			runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, carrier, &result, overrides)
			if failure == "checkpoint" {
				if result.Sequence.Stopped != "host_error" || result.Sequence.NotStarted != 1 {
					t.Fatalf("storage failure continued work: %+v", result)
				}
				if _, err := os.Stat(filepath.Join(transform.directory, "quiet-failure.txt")); !os.IsNotExist(err) {
					t.Fatalf("unpersisted operation applied: %v", err)
				}
			} else if result.CleanupDiagnostic == "" || result.ControlSessionID == 0 {
				t.Fatalf("cleanup failure lost diagnostics/session: %+v", result)
			}
		})
	}
}
