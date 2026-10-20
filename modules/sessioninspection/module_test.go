package sessioninspection_test

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/agentmetrics"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/sessioninspection"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func options(replay string) query.SingleOptions {
	return query.SingleOptions{Session: "supplied.jsonl", ReplayDirectory: replay, Limit: 1, TextBytes: 4096}
}

func decodedCall(workspace, id string) binding.RolloutCall {
	return binding.RolloutCall{HasCall: true, Workspace: workspace, Line: 3, Timestamp: "2026-10-01T00:00:00Z", Model: "test-model",
		Item: binding.RolloutItem{Type: "function_call", CallID: id, Name: "exec_command", Namespace: "functions", Arguments: `{"cmd":"printf recorded"}`}}
}

func retained(call binding.RolloutCall) legacycall.Record {
	return legacycall.Record{Version: 1, Workspace: call.Workspace, CallID: call.Item.CallID,
		History: legacycall.History{ToolName: "exec_command", CarrierName: call.Item.Name, CarrierKind: "function", CarrierPayload: call.Item.Arguments,
			UpstreamItem: map[string]jsontext.Value{"namespace": jsontext.Value(`"functions"`)}, Script: "printf recorded", AlreadySatisfied: true}}
}

func writeRecord(t *testing.T, directory string, call binding.RolloutCall, record legacycall.Record) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, legacycall.Name(call.Workspace, call.Item.CallID, false)), data, 0440); err != nil {
		t.Fatal(err)
	}
}

func scripted(t *testing.T, calls []binding.RolloutCall, contributions ...inspection.Contribution) (*query.Service, *moduletest.Scripted) {
	t.Helper()
	script := &moduletest.Scripted{ReadRollout: func(context.Context, string, func(binding.RolloutEvent) error) (binding.Rollout, error) {
		return binding.Rollout{InspectionMetadata: rollout.InspectionMetadata{ThreadID: "thread-original", Source: []byte(`"cli"`)}, Calls: calls}, nil
	}}
	service := moduletest.New(t, sessioninspection.Module, sessioninspection.Deps{}, module.Slots{Inspection: contributions}, script)
	if script.RolloutRegistrations != 1 {
		t.Fatalf("rollout registrations = %d", script.RolloutRegistrations)
	}
	return service, script
}

// The real descriptor binding validates retained envelopes and host identities,
// even when a matching filename exists. No writer or live backend is involved.
func TestDescriptorRetainedIdentityAndReadOnlyJoin(t *testing.T) {
	for _, tc := range []struct {
		name, wantError string
		mutate          func(*legacycall.Record)
	}{
		{name: "matching"},
		{name: "workspace", wantError: "identity/version mismatch", mutate: func(r *legacycall.Record) { r.Workspace += "-other" }},
		{name: "call", wantError: "identity/version mismatch", mutate: func(r *legacycall.Record) { r.CallID = "other" }},
		{name: "payload", wantError: "payload does not match", mutate: func(r *legacycall.Record) { r.History.CarrierPayload = "other" }},
		{name: "namespace", wantError: "payload does not match", mutate: func(r *legacycall.Record) { r.History.UpstreamItem["namespace"] = []byte(`"unrelated"`) }},
		{name: "carrier", wantError: "invalid replay history identity", mutate: func(r *legacycall.Record) { r.History.CarrierKind = "unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			replay := filepath.Join(root, "retained")
			if err := os.Mkdir(replay, 0750); err != nil {
				t.Fatal(err)
			}
			// Archived workspace identities must remain usable without creating them.
			call := decodedCall(filepath.Join(root, "archived-workspace"), "original-call")
			record := retained(call)
			if tc.mutate != nil {
				tc.mutate(&record)
			}
			writeRecord(t, replay, call, record)
			service, _ := scripted(t, []binding.RolloutCall{call})
			before := tree(t, root)
			result, err := service.Inspect(t.Context(), options(replay))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				if result.Schema != "" {
					t.Fatalf("partial result: %+v", result)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Calls) != 1 {
					t.Fatalf("calls = %+v", result.Calls)
				}
				got := result.Calls[0]
				if got.CallID != call.Item.CallID || got.Workspace != call.Workspace || got.Tool != "exec_command" || got.Replay != "matched" || got.Outcome != "already_satisfied" {
					t.Fatalf("joined call = %+v", got)
				}
			}
			if after := tree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("inspection changed retained tree: before=%v after=%v", before, after)
			}
		})
	}
}

type fileState struct {
	Mode     fs.FileMode
	Modified time.Time
	Data     string
}

func tree(t *testing.T, root string) map[string]fileState {
	t.Helper()
	result := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state := fileState{Mode: info.Mode(), Modified: info.ModTime()}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.Data = string(data)
		}
		result[path] = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDescriptorOptionalContributions(t *testing.T) {
	service, script := scripted(t, []binding.RolloutCall{decodedCall("", "missing")})
	reads := 0
	original := script.ReadRollout
	script.ReadRollout = func(ctx context.Context, path string, observe func(binding.RolloutEvent) error) (binding.Rollout, error) {
		reads++
		return original(ctx, path, observe)
	}
	if service.HasAX() || service.HasProviderUsage() {
		t.Fatal("zero-contribution composition advertises optional features")
	}
	result, err := service.Inspect(t.Context(), options(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(result.Calls) != 1 || result.Calls[0].Replay != "workspace_unavailable" || result.Calls[0].Tool != "functions.exec_command" || len(result.AX) != 0 {
		t.Fatalf("ordinary inspection: reads=%d result=%+v", reads, result)
	}
	for _, request := range []query.SingleOptions{
		{Session: "missing", Limit: 1, TextBytes: 1, AX: true},
		{Session: "missing", Limit: 1, TextBytes: 1, ReadLog: "missing"},
		{Session: "missing", Limit: 1, TextBytes: 1, Defects: "missing"},
	} {
		if _, err := service.Inspect(t.Context(), request); err == nil || !strings.Contains(err.Error(), "agent metrics are unavailable") {
			t.Fatalf("explicit metrics error = %v", err)
		}
	}
	// The absent directory proves explicit usage fails before discovery as well.
	corpusOptions := query.CorpusOptions{Directory: filepath.Join(t.TempDir(), "absent"), Capture: "missing", Since: time.Unix(0, 0), Until: time.Unix(1, 0), Class: "all", Model: "*", Limit: 1}
	if _, err := service.Corpus(t.Context(), corpusOptions); err == nil || !strings.Contains(err.Error(), "transport diagnostics are unavailable") {
		t.Fatalf("explicit capture error = %v", err)
	}
	if reads != 1 {
		t.Fatalf("unavailable contributions opened source: reads=%d", reads)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "rollout.jsonl"), nil, 0440); err != nil {
		t.Fatal(err)
	}
	corpusOptions.Directory, corpusOptions.Capture = directory, ""
	corpusOptions.Since, corpusOptions.Until = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	corpus, err := service.Corpus(t.Context(), corpusOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Sessions) != 1 || corpus.Sessions[0].ProviderUsage.State != "unavailable" || corpus.Sessions[0].UnavailableCalls != 1 {
		t.Fatalf("ordinary corpus = %+v", corpus)
	}
}

func TestDescriptorWholeRolloutAXIgnoresCallPage(t *testing.T) {
	metrics := moduletest.New(t, agentmetrics.Module, agentmetrics.Deps{}, module.Slots{}, new(moduletest.Scripted))
	contribution := moduletest.BindInspection(agentmetrics.Module.Contributes.Inspection[0], metrics)
	calls := []binding.RolloutCall{decodedCall("", "first"), decodedCall("", "second"), decodedCall("", "third")}
	service, script := scripted(t, calls, contribution)
	script.ReadRollout = func(ctx context.Context, path string, observe func(binding.RolloutEvent) error) (binding.Rollout, error) {
		if observe == nil {
			t.Fatal("AX observer was not bound")
		}
		start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		for _, event := range []binding.RolloutEvent{{Kind: "task_started", TurnID: "turn", At: start}, {Kind: "task_complete", TurnID: "turn", At: start.Add(time.Second)}} {
			if err := observe(event); err != nil {
				return binding.Rollout{}, err
			}
		}
		return binding.Rollout{InspectionMetadata: rollout.InspectionMetadata{ThreadID: "thread-original"}, Calls: calls}, nil
	}
	request := options(t.TempDir())
	request.AX, request.CallID = true, "second"
	result, err := service.Inspect(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	var report evidence.InspectionReport
	if err := json.Unmarshal(result.AX, &report); err != nil {
		t.Fatal(err)
	}
	if result.TotalCalls != 1 || len(result.Calls) != 1 || result.Calls[0].CallID != "second" || report.UnmatchedCalls != 3 || report.ThreadID != "thread-original" || report.Completion.CompletedTurns != 1 || report.Completion.DurationMS != 1000 {
		t.Fatalf("filtered page=%+v AX=%+v", result, report)
	}
	request.CallID, request.Offset = "", 1
	paged, err := service.Inspect(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if paged.TotalCalls != 3 || len(paged.Calls) != 1 || paged.Calls[0].CallID != "second" || paged.NextOffset == nil || *paged.NextOffset != 2 || string(paged.AX) != string(result.AX) {
		t.Fatalf("paged=%+v AX=%s", paged, paged.AX)
	}
}

func TestDescriptorBindsOfflineDecoder(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "session.jsonl")
	wire := `{"type":"session_meta","payload":{"id":"original-thread"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"output-only","output":{"kept":true}}}
{"type":"response_item","payload":{"type":"function_call","call_id":"original-call","name":"exec_command","namespace":"functions","arguments":"{}"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"original-call","output":"old"}}
{"type":"response_item","payload":{"type":"function_call_output","call_id":"original-call","output":"last"}}
`
	if err := os.WriteFile(path, []byte(wire), 0440); err != nil {
		t.Fatal(err)
	}
	script := &moduletest.Scripted{ReadRollout: rollout.Inspect}
	service := moduletest.New(t, sessioninspection.Module, sessioninspection.Deps{}, module.Slots{}, script)
	request := options(filepath.Join(directory, "absent-retained"))
	request.Session, request.Limit, request.Field = path, 50, "output"
	before := tree(t, directory)
	result, err := service.Inspect(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if script.RolloutRegistrations != 1 || result.ThreadID != "original-thread" || len(result.Calls) != 2 || result.Calls[0].CallID != "output-only" || result.Calls[0].Text["output"].Text != `{"kept":true}` || result.Calls[1].Tool != "functions.exec_command" || result.Calls[1].Text["output"].Text != "last" {
		t.Fatalf("decoded result = %+v", result)
	}
	if after := tree(t, directory); !reflect.DeepEqual(before, after) {
		t.Fatal("offline inspection changed source tree")
	}
	// A late conflict must abort admission before a selected first page escapes.
	wire += `{"type":"response_item","payload":{"type":"function_call","call_id":"original-call","name":"exec_command","namespace":"functions","arguments":"different"}}` + "\n"
	path = filepath.Join(directory, "conflict.jsonl")
	if err := os.WriteFile(path, []byte(wire), 0440); err != nil {
		t.Fatal(err)
	}
	request.Session = path
	request.Limit = 1
	failed, err := service.Inspect(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "conflicting call identity") || failed.Schema != "" {
		t.Fatalf("late conflict: result=%+v error=%v", failed, err)
	}
}

func TestDescriptorCorpusFindingAdmission(t *testing.T) {
	directory, replay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source.jsonl"), nil, 0440); err != nil {
		t.Fatal(err)
	}
	poll := decodedCall(directory, "poll")
	poll.Item.Name, poll.Item.Arguments = "write_stdin", `{"session_id":42,"chars":""}`
	output, err := json.Marshal("Wall time: 0.1 seconds\nProcess running with session ID 42\nOutput:\n")
	if err != nil {
		t.Fatal(err)
	}
	poll.Outputs = []binding.RolloutItem{{Output: output}}
	calls := []binding.RolloutCall{poll}
	for _, id := range []string{"read-first", "read-second", "read-third"} {
		call := decodedCall(directory, id)
		call.Item.Arguments = `{"cmd":"mcat file.go 1:100"}`
		call.Outputs = []binding.RolloutItem{{Output: []byte(`"read: incomplete; next_call: mread retained"`)}}
		calls = append(calls, call)
	}
	service, _ := scripted(t, calls)
	request := query.CorpusOptions{Directory: directory, ReplayDirectory: replay,
		Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), Model: "*", Class: "all", Limit: 2}
	result, err := service.Corpus(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("corpus = %+v", result)
	}
	session := result.Sessions[0]
	if session.SelectedCalls != 4 || session.OmittedFindings != 1 || len(session.Findings) != 2 ||
		session.Findings[0].Kind != "empty_poll" || session.Findings[0].Calls[0].CallID != "poll" ||
		session.Findings[1].Kind != "truncation_reread" || session.Findings[1].Calls[1].CallID != "read-second" {
		t.Fatalf("finding admission changed ordering or totals: %+v", session)
	}
	// Reaching the display limit must not skip validation of later evidence.
	last := calls[len(calls)-1]
	corrupt := retained(last)
	corrupt.History.CarrierPayload = "different"
	writeRecord(t, replay, last, corrupt)
	result, err = service.Corpus(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 0 || len(result.Unavailable) != 1 || !strings.Contains(result.Unavailable[0].Reason, "payload does not match") {
		t.Fatalf("late corrupt evidence escaped validation: %+v", result)
	}
}

func TestDescriptorPatchOutcomeRequiresCompletedEvidence(t *testing.T) {
	changed := &legacycall.History{ToolName: "apply_patch", ReviewFiles: []legacycall.ReviewFile{{AfterPath: "changed.txt"}}, Report: "changed file"}
	failed := &legacycall.History{ToolName: "apply_patch", TranslationError: "partial patch failure", ReviewFiles: changed.ReviewFiles}
	for _, tc := range []struct {
		name, want               string
		outcomes                 []*legacycall.History
		hostOutput, shellEffects bool
		unconfirmed, failures    uint64
	}{
		{name: "missing", want: "unconfirmed", outcomes: []*legacycall.History{nil}, unconfirmed: 1},
		{name: "host output without retained completion", want: "unconfirmed", outcomes: []*legacycall.History{nil}, hostOutput: true, unconfirmed: 1},
		{name: "partially observed", want: "unconfirmed", outcomes: []*legacycall.History{changed, nil}, unconfirmed: 1},
		{name: "failed with effects", want: "rejected", outcomes: []*legacycall.History{failed}, failures: 1},
		{name: "known failure and missing outcome", want: "rejected", outcomes: []*legacycall.History{failed, nil}, failures: 1, unconfirmed: 1},
		{name: "missing patch with completed shell", want: "unconfirmed", outcomes: []*legacycall.History{nil}, shellEffects: true, unconfirmed: 1},
		{name: "failed patch with completed shell", want: "rejected", outcomes: []*legacycall.History{failed}, shellEffects: true, failures: 1},
		{name: "completed changes", want: "applied", outcomes: []*legacycall.History{changed}},
		{name: "completed no changes", want: "no_changes", outcomes: []*legacycall.History{{ToolName: "apply_patch"}}},
		{name: "already satisfied", want: "already_satisfied", outcomes: []*legacycall.History{{ToolName: "apply_patch", AlreadySatisfied: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, replay := t.TempDir(), t.TempDir()
			const patch = "*** Begin Patch\n*** Add File: changed.txt\n+text\n*** End Patch\n"
			call := binding.RolloutCall{HasCall: true, Workspace: workspace, Item: binding.RolloutItem{Type: "custom_tool_call", Name: "apply_patch", CallID: "patch", Input: patch}}
			if tc.hostOutput {
				call.Outputs = []binding.RolloutItem{{Output: []byte(`"host result"`)}}
			}
			if tc.shellEffects {
				call.Item.Name, call.Item.Input = "exec", fmt.Sprintf("text(await tools.apply_patch(%q)); text(await tools.exec_command({cmd: \"true\"}));", patch)
			}
			parent := legacycall.Record{Version: 1, Workspace: workspace, CallID: "patch", History: legacycall.History{ToolName: call.Item.Name, CarrierName: call.Item.Name, CarrierKind: "custom", CarrierPayload: call.Item.Input, Script: call.Item.Input}}
			if tc.shellEffects {
				parent.History.ExecObservation = &legacycall.Observation{CodeMode: true}
				shellCall := call
				shellCall.Item.CallID = legacycall.ExecCallID("patch", true)
				writeRecord(t, replay, shellCall, legacycall.Record{Version: 1, Workspace: workspace, CallID: shellCall.Item.CallID, History: legacycall.History{ToolName: "exec_command", CorrelationID: "patch\x00exec", ExecOutcome: &legacycall.Outcome{Status: "completed", Coverage: "exact"}, ReviewFiles: changed.ReviewFiles}})
			}
			for index, outcome := range tc.outcomes {
				parent.History.NativePatches = append(parent.History.NativePatches, legacycall.NativePatch{Input: patch})
				if outcome == nil {
					continue
				}
				derived := *outcome
				derived.Script, derived.CorrelationID = patch, fmt.Sprintf("patch\x00%d", index)
				derivedCall := call
				derivedCall.Item.CallID = legacycall.PatchCallID("patch", index)
				writeRecord(t, replay, derivedCall, legacycall.Record{Version: 1, Workspace: workspace, CallID: derivedCall.Item.CallID, History: derived})
			}
			writeRecord(t, replay, call, parent)
			metrics := moduletest.New(t, agentmetrics.Module, agentmetrics.Deps{}, module.Slots{}, new(moduletest.Scripted))
			service, _ := scripted(t, []binding.RolloutCall{call}, moduletest.BindInspection(agentmetrics.Module.Contributes.Inspection[0], metrics))
			request := options(replay)
			request.AX, request.Field = true, "all"
			result, err := service.Inspect(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			var report evidence.InspectionReport
			if err := json.Unmarshal(result.AX, &report); err != nil {
				t.Fatal(err)
			}
			if len(result.Calls) != 1 || result.Calls[0].Outcome != tc.want || report.Edits.Unconfirmed != tc.unconfirmed || report.Edits.Failed != tc.failures {
				t.Fatalf("outcome=%+v edits=%+v; want %s, unconfirmed=%d failures=%d", result.Calls, report.Edits, tc.want, tc.unconfirmed, tc.failures)
			}
		})
	}
}
