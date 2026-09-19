package router

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi"
)

func TestTrackedChangeIDsAndRanges(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ workspace, session, call, want string }{
		{"/w", "first", "one", "amber1"},
		{"/w", "second", "two", "apple1"},
		{"/w", "first", "three", "amber2"},
		{"/w", "fork", "one", "amber1"},
		{"/other", "first", "one", "amber1"},
	} {
		got, err := store.reserveChange(t.Context(), test.workspace, test.session, test.call)
		if err != nil || got != test.want {
			t.Fatalf("%+v: %q, %v", test, got, err)
		}
	}
	got, err := expandChangeRefs([]string{"amber1..amber3", "amber2", "apple1"})
	if err != nil || !reflect.DeepEqual(got, []string{"amber1", "amber2", "amber3", "apple1"}) {
		t.Fatalf("range = %q, %v", got, err)
	}
	for _, ref := range []string{"amber3..amber1", "amber1..apple3", "amber01", "amber0", "hp_a1", "amber1..amber9999999999999999999999", "amber1..amber257", "../amber1"} {
		if _, err := expandChangeRefs([]string{ref}); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
	if changeStreamName(25) != "z" || changeStreamName(26) != "aa" {
		t.Fatal("invalid stream names")
	}
}

func TestTrackedChangeConcurrentReservation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			store, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Error(err)
				return
			}
			id, err := store.reserveChange(t.Context(), "/w", "agent", "call")
			if err != nil || id != "amber1" {
				t.Errorf("id = %q, %v", id, err)
			}
		})
	}
	workers.Wait()
}

func TestTrackedChangesNoOpPendingAndMissing(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), "/w", "a", "noop")
	if err != nil {
		t.Fatal(err)
	}
	options := changeReadOptions{workspace: "/w", ids: []string{id}}
	pending, err := store.readChanges(t.Context(), options)
	if err != nil || !strings.Contains(pending, "pending") {
		t.Fatalf("pending = %q, %v", pending, err)
	}
	history := mekugiHistory{ChangeID: id, CorrelationID: "noop", AlreadySatisfied: true}
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"noop": history}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"noop": history}); err != nil {
			t.Fatal(err)
		}
	}
	text, err := store.readChanges(t.Context(), options)
	if err != nil || text != "amber1 no-op\n" {
		t.Fatalf("no-op = %q, %v", text, err)
	}
	if err := os.Remove(filepath.Join(store.directory, replayRecordName("/w", "noop", false))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readChanges(t.Context(), options); err == nil {
		t.Fatal("silently accepted missing replay record")
	}
}

func TestTrackedChangeCursorAndFilters(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), "/w", "a", "one")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{
		ChangeID: id, CorrelationID: "one", Applied: true,
		ReviewFiles: []mekugi.ReviewFile{{BeforePath: "old", AfterPath: "new", Diff: "wanted\n"}, {AfterPath: "other", Diff: "unrelated\n"}},
	}
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"one": history}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"old", "new"} {
		got, err := store.readChanges(t.Context(), changeReadOptions{workspace: "/w", ids: []string{id}, paths: []string{path}})
		if err != nil || !strings.Contains(got, "wanted") || strings.Contains(got, "unrelated") {
			t.Fatalf("path %s: %q, %v", path, got, err)
		}
	}
}

func TestTrackedChangeReconciliationIsAtomic(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	id, err := store.reserveChange(t.Context(), workspace, transform.threadID, "one")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ToolName: mekugiToolName, Script: testMekugiScript, Patch: testTranslatedPatch, Report: testMekugiReport, ChangeID: id, CorrelationID: "one", CarrierName: "exec"}
	transform.recordLocal("one", &history)
	if err := transform.commitHistory(); err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"input": []any{
			map[string]any{"type": "custom_tool_call_output", "call_id": "one", "output": history.Report},
			map[string]any{"type": "custom_tool_call", "call_id": "one", "name": "wrong-carrier", "input": "wrong"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.reconcileVisibleInput(t.Context(), &request, workspace, "reader"); err == nil {
		t.Fatal("accepted conflicting carrier")
	}
	text, err := store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{history.ChangeID}})
	if err != nil || !strings.Contains(text, "application unconfirmed") {
		t.Fatalf("published partial confirmation: %q, %v", text, err)
	}
}

func TestTrackedChangeQuota(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.maxBytes = 1
	if _, err := store.reserveChange(t.Context(), "/w", "a", "one"); err == nil {
		t.Fatal("accepted index quota overflow")
	}
	if _, err := os.Stat(filepath.Join(store.directory, changeIndexName("/w"))); !os.IsNotExist(err) {
		t.Fatal(fmt.Errorf("published over-quota index: %w", err))
	}
}

func TestTrackedStreamsUseThreadsNotTransportSessions(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ thread, session, call, want string }{
		{"parent", "shared", "one", "amber1"},
		{"child", "shared", "two", "apple1"},
		{"parent", "changed", "three", "amber2"},
	} {
		id, err := store.reserveChange(t.Context(), "/w", test.thread, test.call)
		if err != nil || id != test.want {
			t.Fatalf("%+v: %s, %v", test, id, err)
		}
	}
}

func TestTrackedHistoryIncludesSuccessfulDiagnostics(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), "/w", "a", "one")
	if err != nil {
		t.Fatal(err)
	}
	const warning = "mekugi: warning: outcome hook failed"
	history := mekugiHistory{ChangeID: id, CorrelationID: "one", Report: changeNotice(id) + "in f.txt\n" + warning + "\n"}
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"one": history}); err != nil {
		t.Fatal(err)
	}
	options := changeReadOptions{workspace: "/w", ids: []string{id}, view: "history"}
	text, err := store.readChanges(t.Context(), options)
	if err != nil || strings.Count(text, warning) != 1 || strings.Contains(text, changeNotice(id)) {
		t.Fatalf("history = %q, %v", text, err)
	}
	options.view = ""
	text, err = store.readChanges(t.Context(), options)
	if err != nil || strings.Contains(text, warning) {
		t.Fatalf("default = %q, %v", text, err)
	}
}

func TestTrackedChangeCorruptCounterCannotReplaceID(t *testing.T) {
	for _, corrupt := range []string{"reset", "missing", "advanced"} {
		t.Run(corrupt, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.reserveChange(t.Context(), "/w", "a", "one"); err != nil {
				t.Fatal(err)
			}
			index, err := store.readChangeIndex("/w")
			if err != nil {
				t.Fatal(err)
			}
			switch corrupt {
			case "reset":
				index.Streams[0].Next = 0
			case "advanced":
				index.Streams[0].Next = 2
			case "missing":
				index.Streams = nil
			}
			data, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.directory, changeIndexName("/w"))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.reserveChange(t.Context(), "/w", "a", "two"); err == nil {
				t.Fatal("accepted corrupt stream")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatal("corruption handling changed persisted IDs")
			}
		})
	}
}

func TestTrackedFormerShellPathIsWorkspaceScope(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	if err := os.Mkdir(filepath.Join(workspace, "@shell"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "@shell", "script"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := mekugi.ApplyForHostAt(t.Context(), workspace, []mekugi.FileEdit{{Path: "@shell/script", Script: `type "old" "new"`}}, "")
	if err != nil || !result.Change.Applied {
		t.Fatalf("workspace application = %+v, %v", result, err)
	}
	id, err := store.reserveChange(t.Context(), workspace, transform.threadID, "workspace-shell-path")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ToolName: mekugiToolName, ChangeID: id, CorrelationID: "workspace-shell-path", Applied: true, ReviewFiles: result.ReviewFiles}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"workspace-shell-path": history}); err != nil {
		t.Fatal(err)
	}
	text, err := proxy.replayStore.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{history.ChangeID}})
	if err != nil || strings.Contains(text, "scope: retained shell script") || !strings.Contains(text, "@shell/script") {
		t.Fatalf("workspace scope = %q, %v", text, err)
	}
	files, err := proxy.replayStore.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil || len(files) != 1 || files[0].Path != filepath.Join(workspace, "@shell", "script") {
		t.Fatalf("workspace live diff = %#v, %v", files, err)
	}
}

func TestTrackedNativeFailureIncludesChangeID(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is unavailable: %v", err)
	}
	history := mekugiHistory{ChangeID: "amber1", Patch: "a proposed patch\n", Report: "change amber1\nsuccess report\n"}
	script := "apply_patch() { cat >/dev/null; printf 'executor failed\\n'; return 7; }\n" + mekugiNativeCommand(history)
	output, err := exec.CommandContext(t.Context(), bash, "-c", script).CombinedOutput()
	if err == nil || string(output) != "change amber1\nexecutor failed\n" {
		t.Fatalf("failure output = %q, %v", output, err)
	}
}

func TestTrackedHostEnvelopeConfirmation(t *testing.T) {
	for _, carrier := range []string{nativeExecCommandToolName, "exec"} {
		t.Run(carrier, func(t *testing.T) {
			transform, proxy, _, workspace := newMekugiTestTransform(t)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			history := mekugiHistory{ToolName: mekugiToolName, Script: testMekugiScript, Patch: testTranslatedPatch, Report: testMekugiReport, CarrierName: "exec"}
			if err := transform.commitHistory(); err != nil {
				t.Fatal(err)
			}
			// Use a separate durable chain with the actual carrier identity.
			id, err := store.reserveChange(t.Context(), workspace, "host", "host-edit")
			if err != nil {
				t.Fatal(err)
			}
			history.ChangeID, history.CorrelationID = id, "host-edit"
			history.CarrierName = carrier
			history.CarrierKind = codeModeCarrierFunction
			history.Report = changeNotice(id) + strings.TrimPrefix(history.Report, changeNotice("amber1"))
			if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"host-edit": history}); err != nil {
				t.Fatal(err)
			}
			// The host's prepend_script_status inserts metadata as its own
			// input_text block, followed by the carrier's text(report).
			output := []any{
				map[string]any{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
				map[string]any{"type": "input_text", "text": history.Report},
			}
			if carrier == nativeExecCommandToolName {
				output = []any{map[string]any{"type": "input_text", "text": "Chunk ID: abc\nWall time: 0.1000 seconds\nProcess exited with code 0\nOriginal token count: 42\nOutput:\n" + history.Report}}
			}
			request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
				"input": []any{map[string]any{
					"type": "function_call_output", "call_id": "host-edit",
					"output": output,
				}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.reconcileVisibleInput(t.Context(), &request, workspace, "reader"); err != nil {
				t.Fatal(err)
			}
			// A new reader process sees the persisted receipt, not a live cache.
			store, err = openMekugiReplayStore(store.directory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.readChanges(t.Context(), changeReadOptions{workspace: workspace, ids: []string{id}})
			if err != nil || !strings.HasPrefix(got, id+" applied\n") {
				t.Fatalf("receipt = %q, %v", got, err)
			}
		})
	}
}

func TestExactHostReportEvidence(t *testing.T) {
	const report = "change amber1\nsuccess\n"
	native := "Wall time: 0.1000 seconds\nProcess exited with code 0\nOutput:\n"
	completed := "Script completed\nWall time 0.1 seconds\nOutput:\n"
	for _, test := range []struct {
		name, carrier, output string
		want                  bool
	}{
		{"bare", "exec", report, true},
		{"native", nativeExecCommandToolName, native + report, true},
		{"code", "exec", completed + report, true},
		{"wrong carrier", nativeExecCommandToolName, completed + report, false},
		{"failed", nativeExecCommandToolName, strings.Replace(native, "code 0", "code 1", 1) + report, false},
		{"running", nativeExecCommandToolName, strings.Replace(native, "Process exited with code 0", "Process running with session ID 42", 1) + report, false},
		{"code failed", "exec", strings.Replace(completed, "completed", "failed", 1) + report, false},
		{"prefix", "exec", "untrusted\n" + completed + report, false},
		{"suffix", "exec", completed + report + "extra\n", false},
		{"truncated", nativeExecCommandToolName, native + "Warning: truncated output\n" + report, false},
		{"empty", "exec", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			history := mekugiHistory{CarrierName: test.carrier, Report: report}
			if got := history.confirmsReport(mustMarshalJSON(test.output)); got != test.want {
				t.Fatalf("confirmation = %v; want %v", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		header, body string
		extra        bool
		want         bool
	}{
		{completed, report, false, true},
		{completed + "extra", report, false, false},
		{completed, report + "extra", false, false},
		{strings.Replace(completed, "completed", "failed", 1), report, false, false},
		{"Script running with cell ID 42\nWall time 0.1 seconds\nOutput:\n", report, false, false},
		{completed, report, true, false},
	} {
		blocks := []any{
			map[string]any{"type": "input_text", "text": test.header},
			map[string]any{"type": "input_text", "text": test.body},
		}
		if test.extra {
			blocks = append(blocks, map[string]any{"type": "input_text", "text": "extra"})
		}
		if got := (mekugiHistory{Report: report, CarrierName: "exec"}).confirmsReport(mustMarshalJSON(blocks)); got != test.want {
			t.Errorf("multipart %+v: got %v", test, got)
		}
	}
	history := mekugiHistory{Report: report}
	if history.confirmsReport(mustMarshalJSON([]any{
		map[string]any{"type": "input_text", "text": report},
		map[string]any{"type": "input_text", "text": "extra"},
	})) {
		t.Fatal("accepted multiple output blocks")
	}
	if (mekugiHistory{}).confirmsReport(mustMarshalJSON("")) {
		t.Fatal("empty report confirmed")
	}
}

func TestChangeReadContinuationRetainsOnlyDescriptor(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), "/w", "a", "one")
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{
		ChangeID: id, CorrelationID: "one", ToolName: mekugiToolName,
		ReviewFiles: []mekugi.ReviewFile{{AfterPath: "file", Diff: strings.Repeat("+line\n", 100)}},
	}
	histories := map[string]mekugiHistory{"one": history}
	if err := store.put(t.Context(), "/w", histories); err != nil {
		t.Fatal(err)
	}
	options := changeReadOptions{workspace: "/w", ids: []string{id}}
	text, err := store.readChanges(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.putChangeRead(t.Context(), options, text, 6)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.readShellOutput(t.Context(), ref)
	if err != nil || record.Changes == nil || record.Stdout != "" || record.Stderr != "" {
		t.Fatalf("not a descriptor-only read: %#v, %v", record, err)
	}
	output, err := store.readSourceStreams(t.Context(), record)
	if err != nil || output.Stdout != text[6:] {
		t.Fatalf("read remainder: %q, %v", output.Stdout, err)
	}
	history.confirmed = true
	histories["one"] = history
	if err := store.confirmChanges(t.Context(), "/w", histories); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readSourceStreams(t.Context(), record); err == nil {
		t.Fatal("changed projection accepted")
	}
}
