package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCorpusFixture(t *testing.T, directory, name, model string, items ...map[string]any) string {
	t.Helper()
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	emit := func(kind string, payload any) {
		t.Helper()
		if err := encoder.Encode(map[string]any{
			"timestamp": "2026-09-15T10:00:00Z", "type": kind, "payload": payload}); err != nil {
			t.Fatal(err)
		}
	}
	emit("session_meta", map[string]any{"id": name, "cwd": directory, "source": "cli"})
	emit("turn_context", map[string]any{"model": model, "cwd": directory})
	for _, item := range items {
		emit("response_item", item)
	}
	path := filepath.Join(directory, name+".jsonl")
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCorpusFixture(t *testing.T, root string, extra ...string) (sessionCorpus, int, string) {
	t.Helper()
	var out, diagnostic bytes.Buffer
	args := []string{"--sessions-dir", root, "--replay-dir", root, "--since", "2026-09-14T00:00:00Z", "--until", "2026-09-16T00:00:00Z"}
	status := RunSessionCorpusInspection(t.Context(), append(args, extra...), &out, &diagnostic)
	var result sessionCorpus
	if status == 0 {
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	} else if out.Len() != 0 {
		t.Fatal("failure published partial JSON")
	}
	return result, status, diagnostic.String()
}

func TestSessionCorpusFiltersAndEmptyPoll(t *testing.T) {
	root := t.TempDir()
	poll := map[string]any{"type": "function_call", "name": "write_stdin", "call_id": "poll", "arguments": `{"session_id":42,"chars":""}`}
	output := map[string]any{"type": "function_call_output", "call_id": "poll", "output": "Wall time: 5 seconds\nProcess running with session ID 42\nOutput:\n"}
	path := writeCorpusFixture(t, root, "astra", "gpt-6-astra", poll, output)
	writeCorpusFixture(t, root, "grok", "grok-4.6", poll, output)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, status, diagnostic := runCorpusFixture(t, root, "--exclude-model", "*grok*")
	if status != 0 || len(result.Sessions) != 1 || result.Excluded["model"] != 1 {
		t.Fatalf("%d %s %+v", status, diagnostic, result)
	}
	session := result.Sessions[0]
	if session.UnavailableCalls != 1 || session.ProviderUsage.State != "unavailable" ||
		len(session.Findings) != 1 || session.Findings[0].Kind != "empty_poll" || session.Findings[0].Calls[0].Line != 3 {
		t.Fatalf("lost evidence state: %+v", session)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed rollout")
	}
	if _, err := os.Stat(filepath.Join(root, "store.lock")); !os.IsNotExist(err) {
		t.Fatal("inspection created replay lock")
	}
	result, status, _ = runCorpusFixture(t, root, "--since", "2026-09-15T10:00:01Z")
	if status != 0 || len(result.Sessions) != 0 {
		t.Fatalf("time filtering: %+v", result)
	}
	if _, status, _ := runCorpusFixture(t, root, "--model", "["); status != 2 {
		t.Fatal("accepted invalid model glob")
	}
}

func TestSessionCorpusRecoveryAndReadCandidates(t *testing.T) {
	root := t.TempDir()
	edit := inspectionFixture(t, root, "edit", mekugiHistory{ToolName: "hpatch", Root: root, Script: "bad",
		TranslationError: "rejected", CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: "edit carrier",
		CorrelationID: "chain", Attempt: 1})
	recovery := inspectionFixture(t, root, "recovery", mekugiHistory{ToolName: "hpatch_recover", Root: root, Script: "fixed",
		CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: "recovery carrier", CorrelationID: "chain", Attempt: 2})
	first := inspectionFixture(t, root, "first", mekugiHistory{ToolName: "shell", Root: root, Script: "hcat file.go 1:100",
		CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: "read carrier"})
	second := inspectionFixture(t, root, "second", mekugiHistory{ToolName: "shell", Root: root, Script: "hcat file.go 50:150",
		CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: "reread carrier"})
	writeCorpusFixture(t, root, "session", "gpt-6-astra", edit, recovery, first,
		map[string]any{"type": "custom_tool_call_output", "call_id": "first", "output": "read: incomplete; next_call: hread r_123"},
		second)
	result, status, diagnostic := runCorpusFixture(t, root)
	if status != 0 || len(result.Sessions) != 1 || len(result.Sessions[0].Findings) != 2 {
		t.Fatalf("%d %s %+v", status, diagnostic, result)
	}
	findings := result.Sessions[0].Findings
	if findings[0].Kind != "recovery_chain" || findings[0].CallCount != 2 || findings[0].Rejected != 1 ||
		findings[1].Kind != "truncation_reread" || !findings[1].Candidate {
		t.Fatalf("%+v", findings)
	}
	result, status, _ = runCorpusFixture(t, root, "--limit", "1")
	if status != 0 || result.Sessions[0].OmittedFindings != 1 {
		t.Fatalf("unreported omission: %+v", result)
	}
}

func TestSessionCorpusMissingEvidenceAndClassification(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.jsonl"), []byte("not json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, status, _ := runCorpusFixture(t, root)
	if status != 0 || len(result.Unavailable) != 1 {
		t.Fatalf("%d %+v", status, result)
	}
	for _, test := range []struct{ source, cwd, want string }{
		{`"cli"`, "/project", "production"}, {`"exec"`, "/project", "probe"},
		{`{"subagent":{}}`, "/project", "production"}, {`null`, "/project", "unknown"},
		{`"cli"`, os.TempDir(), "probe"},
		{`"cli"`, filepath.Clean(os.TempDir()) + "-project", "production"},
		{`"cli"`, filepath.Join(os.TempDir(), "probe"), "probe"},
	} {
		if got := classifyCorpusSession(sessionAXInput{Source: json.RawMessage(test.source), Cwd: test.cwd}); got != test.want {
			t.Fatalf("%s != %s", got, test.want)
		}
	}
	reads := corpusReadSelections("hcat 'file name' 1:20\nhcat --max-tokens 200 other 30:40")
	if len(reads) != 2 || !corpusReadOverlap(reads, corpusReadSelections("hcat 'file name' 10:50")) ||
		corpusReadOverlap(reads, corpusReadSelections("hcat 'file name' 21:50")) || len(corpusReadSelections("echo 'hcat file name'")) != 0 {
		t.Fatalf("%+v", reads)
	}
	if strings.Contains(result.UsageScope, "estimated") {
		t.Fatal("usage must not infer token costs")
	}
}
