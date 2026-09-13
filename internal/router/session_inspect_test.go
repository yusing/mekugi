package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func writeInspectionSession(t *testing.T, directory string, items ...map[string]any) string {
	t.Helper()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, item := range items {
		if err := encoder.Encode(map[string]any{"type": "response_item", "payload": item}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(directory, "rollout.jsonl")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionInspectionExactSizeWithoutFinalNewline(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	writer := bufio.NewWriter(file)
	const recordBytes = 65536
	line := "{}" + strings.Repeat(" ", recordBytes-3)
	for i := range maxSessionInspectionBytes / recordBytes {
		delimiter := "\n"
		if i == maxSessionInspectionBytes/recordBytes-1 {
			delimiter = " "
		}
		if _, err := writer.WriteString(line + delimiter); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if calls, err := readSessionInspection(t.Context(), path, nil); err != nil || len(calls) != 0 {
		t.Fatalf("boundary session: %d calls, %v", len(calls), err)
	}
	ctx := &inspectionGrowingEvidenceContext{Context: t.Context(), grow: func() {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString("\n"); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := readSessionInspection(ctx, path, nil); err == nil || !strings.Contains(err.Error(), "exceeds 64 MiB") {
		t.Fatalf("growing session accepted: %v", err)
	}
}

// Append after descriptor validation, at the first per-record cancellation check.
type inspectionGrowingEvidenceContext struct {
	context.Context
	grow func()
}

func (ctx *inspectionGrowingEvidenceContext) Err() error {
	if ctx.grow != nil {
		ctx.grow()
		ctx.grow = nil
	}
	return ctx.Context.Err()
}

func inspectionFixture(t *testing.T, directory, id string, history mekugiHistory) map[string]any {
	t.Helper()
	record := replayRecord{Version: 1, Workspace: directory, CallID: id, History: durableHistory(history)}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, replayRecordName(directory, id, false)), data, 0600); err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"type": carrierItemType(history.effectiveCarrierKind()), "call_id": id,
		"name": history.carrierName, carrierPayloadField(history.effectiveCarrierKind()): history.carrierInput(),
	}
}

func inspectFixture(t *testing.T, root, session string, extra ...string) (sessionInspection, int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := []string{"--workspace", root, "--session", session, "--replay-dir", root}
	code := RunSessionInspection(t.Context(), append(args, extra...), &stdout, &stderr)
	var result sessionInspection
	if code == 0 {
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
	} else if stdout.Len() != 0 {
		t.Fatal("failure published partial result")
	}
	return result, code, stderr.String()
}

func TestSessionInspectionLogicalCallsAndConfirmation(t *testing.T) {
	root := t.TempDir()
	history := mekugiHistory{
		toolName: "hpatch", root: root, script: "in file.txt\ntype \"old\" \"new\"\n",
		patch: "*** Begin Patch\n*** End Patch\n", report: "applied report",
		carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: `{"code":"private carrier"}`,
		correlationID: "chain", attempt: 1,
	}
	call := inspectionFixture(t, root, "call_edit", history)
	rejected := mekugiHistory{
		toolName: "hpatch_recover", root: root, script: `type "bad" "new"`,
		evaluated:        "in file.txt\ntype \"missing\" \"new\"\n",
		translationError: "target missing", evaluatorRejected: true, report: "rejected",
		carrierName: "exec", carrierKind: codeModeCarrierCustom, carrierPayload: "diagnostic carrier",
		correlationID: "chain", attempt: 2,
		rejections: []mekugi.HostRejection{{Command: 2, Reason: "literal-missing"}},
	}
	recovery := inspectionFixture(t, root, "call_recovery", rejected)
	session := writeInspectionSession(t, root, call,
		map[string]any{"type": "function_call_output", "call_id": "call_edit", "output": "applied report"},
		recovery)
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result, code, diagnostic := inspectFixture(t, root, session, "--field", "all")
	if code != 0 || len(result.Calls) != 2 {
		t.Fatalf("code %d: %s, result %+v", code, diagnostic, result)
	}
	first, second := result.Calls[0], result.Calls[1]
	if first.Tool != "hpatch" || first.Outcome != "confirmed" || first.Text["script"].Text != history.script ||
		first.Text["patch"].Text != history.patch || second.Tool != "hpatch_recover" ||
		second.Outcome != "rejected" || second.Text["evaluated"].Text != rejected.evaluated ||
		second.Rejections != 1 || !strings.Contains(second.Text["rejections"].Text, "literal-missing") {
		t.Fatalf("logical projection = %+v", result)
	}
	after, _ := os.ReadDir(root)
	if len(before) != len(after) {
		t.Fatal("inspection created store files")
	}
	if _, err := os.Stat(filepath.Join(root, "store.lock")); !os.IsNotExist(err) {
		t.Fatal("inspection created a lock")
	}
	for _, entry := range after {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatalf("changed mode on %s", entry.Name())
		}
	}
}

func TestSessionInspectionMissingBoundsAndPagination(t *testing.T) {
	root := t.TempDir()
	first := map[string]any{"type": "custom_tool_call", "call_id": "one", "name": "shell", "input": "ééé"}
	second := map[string]any{"type": "function_call", "call_id": "two", "name": "native", "arguments": "{}"}
	session := writeInspectionSession(t, root, first, first, second)
	result, code, diagnostic := inspectFixture(t, root, session, "--limit", "1")
	if code != 0 || result.TotalCalls != 2 || result.NextOffset == nil || *result.NextOffset != 1 {
		t.Fatalf("pagination: %+v code %d %s", result, code, diagnostic)
	}
	call := result.Calls[0]
	if call.Replay != "missing" || call.Outcome != "unavailable" || call.Text["script"].Text != "" ||
		call.Text["script"].Bytes != 6 || call.Text["script"].OmittedBytes != 6 {
		t.Fatalf("missing/default projection: %+v", call)
	}
	result, code, diagnostic = inspectFixture(t, root, session, "--call-id", "one", "--field", "script", "--text-bytes", "3")
	if code != 0 || result.Calls[0].Text["script"].Text != "é" || result.Calls[0].Text["script"].OmittedBytes != 4 {
		t.Fatalf("prefix: %+v code %d %s", result, code, diagnostic)
	}
	result, code, _ = inspectFixture(t, root, session, "--offset", strconv.Itoa(int(^uint(0)>>1)))
	if code != 0 || len(result.Calls) != 0 || result.NextOffset != nil {
		t.Fatalf("empty page: %+v code %d", result, code)
	}
}

func TestSessionInspectionOutcomeEvidence(t *testing.T) {
	for _, test := range []struct {
		name, output, want string
		applied, satisfied bool
	}{
		{name: "missing", want: "translated_unconfirmed"},
		{name: "failed", output: "host failed", want: "translated_unconfirmed"},
		{name: "exact", output: "report", want: "confirmed"},
		{name: "applied", applied: true, want: "applied"},
		{name: "satisfied", satisfied: true, want: "already_satisfied"},
		{name: "applied with report", applied: true, output: "report", want: "applied"},
		{name: "satisfied with report", satisfied: true, output: "report", want: "already_satisfied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			h := mekugiHistory{toolName: "hpatch", root: root, script: "script", patch: "patch", report: "report",
				carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}",
				applied: test.applied, alreadySatisfied: test.satisfied}
			call := inspectionFixture(t, root, "call", h)
			items := []map[string]any{call}
			if test.output != "" {
				items = append(items, map[string]any{"type": "function_call_output", "call_id": "call", "output": test.output})
			}
			result, code, diagnostic := inspectFixture(t, root, writeInspectionSession(t, root, items...))
			if code != 0 || result.Calls[0].Outcome != test.want {
				t.Fatalf("result %+v code %d %s", result, code, diagnostic)
			}
		})
	}
}
func TestSessionInspectionRejectsMismatchesAndCorruption(t *testing.T) {
	for _, mode := range []string{"payload", "corrupt", "duplicate", "json", "utf8"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			h := mekugiHistory{toolName: "shell", root: root, script: "echo safe",
				carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}"}
			call := inspectionFixture(t, root, "call", h)
			items := []map[string]any{call}
			if mode == "payload" {
				call["arguments"] = "changed"
			}
			if mode == "duplicate" {
				items = append(items, map[string]any{"type": "function_call", "call_id": "call", "name": "other", "arguments": "{}"})
			}
			session := writeInspectionSession(t, root, items...)
			switch mode {
			case "corrupt":
				os.WriteFile(filepath.Join(root, replayRecordName(root, "call", false)), []byte("{}"), 0600)
			case "json":
				os.WriteFile(session, []byte("{bad\n"), 0600)
			case "utf8":
				os.WriteFile(session, []byte{0xff, '\n'}, 0600)
			}
			_, code, _ := inspectFixture(t, root, session)
			if code != 1 {
				t.Fatalf("code = %d", code)
			}
		})
	}
}

func TestSessionInspectionOutputOnlyAndOriginalCalls(t *testing.T) {
	root := t.TempDir()
	h := mekugiHistory{toolName: "hpatch", root: root, script: "original", report: "report",
		carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}",
		upstreamItem: map[string]json.RawMessage{
			"type": mustTestJSON(t, "custom_tool_call"), "name": mustTestJSON(t, "hpatch"), "input": mustTestJSON(t, "original"),
		}}
	inspectionFixture(t, root, "call", h)
	for _, original := range []bool{false, true} {
		items := []map[string]any{{"type": "function_call_output", "call_id": "call", "output": "report"}}
		if original {
			items = append(items, map[string]any{"type": "custom_tool_call", "call_id": "call", "name": "hpatch", "input": "original"})
		}
		result, code, diagnostic := inspectFixture(t, root, writeInspectionSession(t, root, items...))
		if code != 0 || result.Calls[0].Outcome != "confirmed" {
			t.Fatalf("result %+v code %d %s", result, code, diagnostic)
		}
	}
	other := t.TempDir()
	session := writeInspectionSession(t, other, map[string]any{"type": "function_call_output", "call_id": "call", "output": "report"})
	result, code, diagnostic := inspectFixture(t, other, session, "--replay-dir", root)
	if code != 0 || result.Calls[0].Replay != "missing" {
		t.Fatalf("workspace isolation: %+v code %d %s", result, code, diagnostic)
	}
}

func TestSessionInspectionNamespaceIdentity(t *testing.T) {
	for _, kind := range []string{"original", "carrier", "duplicate", "native"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			h := mekugiHistory{toolName: "shell", root: root, script: "echo safe",
				carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}",
				upstreamItem: map[string]json.RawMessage{
					"type": mustTestJSON(t, "function_call"), "name": mustTestJSON(t, "shell"),
					"namespace": mustTestJSON(t, "functions"), "arguments": mustTestJSON(t, "echo safe"),
				}}
			call := inspectionFixture(t, root, "call", h)
			call["namespace"] = "functions"
			if kind == "original" {
				call["name"], call["arguments"] = "shell", "echo safe"
			}
			if kind == "native" {
				call["call_id"] = "native"
			}
			result, code, diagnostic := inspectFixture(t, root, writeInspectionSession(t, root, call))
			if code != 0 {
				t.Fatalf("matching namespace rejected: %s", diagnostic)
			}
			if kind == "native" && result.Calls[0].Tool != "functions.exec" {
				t.Fatalf("native namespace lost: %+v", result.Calls[0])
			}
			changed := map[string]any{"type": call["type"], "call_id": call["call_id"],
				"name": call["name"], "arguments": call["arguments"], "namespace": "other"}
			items := []map[string]any{changed}
			if kind == "duplicate" || kind == "native" {
				items = append([]map[string]any{call}, items...)
			}
			_, code, _ = inspectFixture(t, root, writeInspectionSession(t, root, items...))
			if code != 1 {
				t.Fatal("accepted conflicting namespace")
			}
		})
	}
}

func TestSessionInspectionThreadIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata string
		want     string
	}{
		{"thread ID preferred", `{"id":"thread","session_id":"root"}`, "thread"},
		{"legacy ID", `{"id":"thread"}`, "thread"},
		{"root session ID alone cannot attribute a thread", `{"session_id":"root"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			session := filepath.Join(root, "rollout.jsonl")
			if err := os.WriteFile(session, []byte(`{"type":"session_meta","payload":`+test.metadata+"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result, code, diagnostic := inspectFixture(t, root, session, "--ax")
			if code != 0 || result.AX == nil || result.AX.ThreadID != test.want {
				t.Fatalf("result %+v code %d: %s", result, code, diagnostic)
			}
		})
	}
}

func TestSessionInspectionArgumentAndFileLimits(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--session", "x", "--workspace", "x", "--limit", "0"},
		{"--session", "x", "--workspace", "x", "--field", "unknown"},
	} {
		if code := RunSessionInspection(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Fatalf("args %v: code %d", args, code)
		}
	}
	if code := RunSessionInspection(t.Context(), []string{"--help"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("help code %d", code)
	}
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "large.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxSessionInspectionBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := readSessionInspection(t.Context(), file.Name(), nil); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestSessionInspectionPreservesScannerError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long-line.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxReplayRecordBytes); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = readSessionInspection(t.Context(), path, nil)
	if !errors.Is(err, bufio.ErrTooLong) || !strings.Contains(err.Error(), strconv.Itoa(maxReplayRecordBytes)) {
		t.Fatalf("scanner error or configured limit lost: %v", err)
	}
}

func TestSessionInspectionRejectsOutputOnlyInvalidHistory(t *testing.T) {
	for _, kind := range []codeModeCarrierKind{"", "unknown"} {
		root := t.TempDir()
		history := mekugiHistory{}
		if kind != "" {
			history = mekugiHistory{toolName: "shell", carrierName: "exec", carrierKind: kind}
		}
		inspectionFixture(t, root, "call", history)
		session := writeInspectionSession(t, root, map[string]any{
			"type": "function_call_output", "call_id": "call", "output": "untrusted",
		})
		if _, code, _ := inspectFixture(t, root, session); code != 1 {
			t.Fatalf("invalid history kind %q: code %d", kind, code)
		}
	}
}

func TestSessionInspectionInfersWorkspacePerCall(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	firstHistory := mekugiHistory{toolName: "shell", script: "first",
		carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}"}
	secondHistory := mekugiHistory{toolName: "shell", script: "second",
		carrierName: "exec", carrierKind: codeModeCarrierFunction, carrierPayload: "{}"}
	first := inspectionFixture(t, firstRoot, "first", firstHistory)
	second := inspectionFixture(t, secondRoot, "second", secondHistory)
	// One replay directory, with independently workspace-scoped records.
	recordPath := replayRecordName(secondRoot, "second", false)
	record, err := os.ReadFile(filepath.Join(secondRoot, recordPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, recordPath), record, 0600); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, record := range []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"cwd": firstRoot}},
		{"type": "response_item", "payload": first},
		{"type": "turn_context", "payload": map[string]any{"cwd": secondRoot}},
		{"type": "response_item", "payload": second},
	} {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	session := filepath.Join(firstRoot, "inferred.jsonl")
	if err := os.WriteFile(session, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(t.Context(), []string{"--session", session, "--replay-dir", firstRoot, "--field", "script"}, &stdout, &stderr)
	var result sessionInspection
	if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || len(result.Calls) != 2 {
		t.Fatalf("code %d: %s, output %s", code, stderr.String(), stdout.String())
	}
	if result.WorkspaceOverride != "" || result.Calls[0].Workspace != firstRoot ||
		result.Calls[1].Workspace != secondRoot || result.Calls[0].Text["script"].Text != "first" ||
		result.Calls[1].Text["script"].Text != "second" {
		t.Fatalf("inference = %+v", result)
	}

	override, code, diagnostic := inspectFixture(t, firstRoot, session)
	if code != 0 || override.WorkspaceOverride != firstRoot || override.Calls[1].Replay != "missing" {
		t.Fatalf("override = %+v, code %d: %s", override, code, diagnostic)
	}
}

func TestSessionInspectionWithoutWorkspaceMetadata(t *testing.T) {
	root := t.TempDir()
	session := writeInspectionSession(t, root, map[string]any{
		"type": "custom_tool_call", "call_id": "call", "name": "shell", "input": "echo example",
	})
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(t.Context(), []string{"--session", session, "--replay-dir", root}, &stdout, &stderr)
	var result sessionInspection
	if code != 0 || json.Unmarshal(stdout.Bytes(), &result) != nil || len(result.Calls) != 1 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if result.Calls[0].Replay != "workspace_unavailable" || result.Calls[0].Workspace != "" {
		t.Fatalf("guessed a workspace: %+v", result)
	}
}

func TestSessionInspectionHistoricalWorkspace(t *testing.T) {
	root := t.TempDir()
	absent := filepath.Join(root, "absent")
	if got, err := canonicalInspectionWorkspace(absent); err != nil || got != absent {
		t.Fatalf("absent historical path = %q, %v", got, err)
	}
	if _, err := canonicalInspectionWorkspace("relative"); err == nil {
		t.Fatal("relative metadata accepted")
	}
}

func TestSessionInspectionReadsProducedRecoveryRecords(t *testing.T) {
	transform, _, _, root := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rejected, err := transform.translate("rejected", "in file.txt\ntype \"missing\" \"new\"\n", nil)
	if err != nil || !rejected.evaluatorRejected {
		t.Fatalf("translate rejection: %+v, %v", rejected, err)
	}
	const correction = `type "missing" "old"`
	fixed, err := transform.translateRecovery("fixed", correction, nil)
	if err != nil || fixed.translationError != "" || fixed.patch == "" {
		t.Fatalf("translate correction: %+v, %v", fixed, err)
	}
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), root, map[string]mekugiHistory{"rejected": rejected, "fixed": fixed}); err != nil {
		t.Fatal(err)
	}
	items := []map[string]any{}
	for _, entry := range []struct {
		id      string
		history mekugiHistory
	}{{"rejected", rejected}, {"fixed", fixed}} {
		items = append(items, map[string]any{
			"type": carrierItemType(entry.history.effectiveCarrierKind()), "call_id": entry.id,
			"name": entry.history.carrierName, carrierPayloadField(entry.history.effectiveCarrierKind()): entry.history.carrierInput(),
		})
	}
	result, code, diagnostic := inspectFixture(t, root, writeInspectionSession(t, root, items...),
		"--replay-dir", store.directory, "--field", "all")
	if code != 0 || len(result.Calls) != 2 || result.Calls[0].Outcome != "rejected" ||
		result.Calls[1].Outcome != "translated_unconfirmed" || result.Calls[1].Text["script"].Text != correction ||
		result.Calls[1].Text["evaluated"].Text != fixed.evaluated || result.Calls[1].Text["patch"].Text != fixed.patch {
		t.Fatalf("produced record projection: %+v, code %d: %s", result, code, diagnostic)
	}
}
