package executionobservation

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

func TestCompactRecoveryAuthorityAndPrewarm(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	ref, err := p.StoreCompact(t.Context(), "root", "retained handover", `{"sources":[{"path":"selected.txt"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	recover := func(thread, parent, fork, reference string, warm bool) error {
		req, err := codexresponses.ParseRequest([]byte(fmt.Sprintf(`{"input":[{"type":"compaction","encrypted_content":%q}],"tools":[],"generate":false}`, reference)))
		if err != nil {
			return err
		}
		meta := codexresponses.TurnMetadata{ThreadID: thread, ParentThreadID: parent, ForkedFromThreadID: fork, RequestKind: "turn"}
		if parent != "" {
			meta.SubagentKind = "thread_spawn"
		}
		_, _, err = p.prepareCompactInput(t.Context(), &req, "routing", thread, meta, warm)
		if err == nil && (!bytes.Contains(req.Fields["input"], []byte("retained handover")) || bytes.Contains(req.Fields["input"], []byte("selected.txt"))) {
			t.Fatal("summary recovery or metadata separation failed")
		}
		return err
	}
	if err := recover("warm-fork", "", "root", ref, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := p.retained.Store.ReadScope("warm-fork"); err != nil || found {
		t.Fatal("prewarm published inheritance", err)
	}
	for _, tc := range []struct{ thread, parent, fork string }{{"root", "", ""}, {"fork", "", "root"}, {"child", "root", ""}, {"fork", "", ""}, {"child", "", ""}} {
		if err := recover(tc.thread, tc.parent, tc.fork, ref, false); err != nil {
			t.Fatal(tc, err)
		}
	}
	if err := recover("foreign", "", "", ref, false); err == nil {
		t.Fatal("foreign reference accepted")
	}
	later, err := p.StoreCompact(t.Context(), "root", "retained handover", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := recover("fork", "", "", later, false); err == nil {
		t.Fatal("frozen fork accepted later allocation")
	}
	if err := recover("child", "", "", later, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.retained.Store.Directory(), compact.Name(ref))
	if err := os.WriteFile(path, []byte(`{"Version":1,"Reference":"wrong","Summary":"wrong"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recover("root", "", "", ref, false); err == nil {
		t.Fatal("corrupt record accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := recover("root", "", "", ref, false); err == nil {
		t.Fatal("missing record accepted")
	}
}

func TestPendingCompactForkHasIndependentCallIdentity(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	workspace := t.TempDir()
	ref, err := p.StoreCompact(t.Context(), "root", "handover", `{"sources":[{"path":"selected.txt"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(thread, fork string) *mekugiResponseTransform {
		req, e := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "instructions": testBaseInstructions, "tools": testExecResponsesTools(), "input": []any{map[string]any{"type": "compaction", "encrypted_content": ref}}}))
		if e != nil {
			t.Fatal(e)
		}
		meta := codexresponses.TurnMetadata{ThreadID: thread, ForkedFromThreadID: fork, RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: json.RawMessage(`{}`)}}
		tr, e := p.prepareRequest(t.Context(), &req, "routing-"+thread, thread, meta, true)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(tr.Close)
		return tr
	}
	root := prepare("root", "")
	fork := prepare("fork", "root")
	for _, tr := range []*mekugiResponseTransform{root, fork} {
		var call map[string]any
		if err := json.Unmarshal(tr.compactReload, &call); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.TransformJSON(mustTestJSON(t, map[string]any{"id": "resp-" + tr.shellThreadID, "status": "completed", "output": []any{call}})); err != nil {
			t.Fatalf("publish synthetic reader for %s: %v", tr.shellThreadID, err)
		}
	}
}

func TestCompactReaderCallIDsFitOpenAI(t *testing.T) {
	record := compact.Record{Reference: "mekugi:compact:thread:amber", Selection: `{"sources":[{"path":"selected.txt"}]}`}
	raw, _, err := compactReaderCall(record, nil, "01a162ad-ee92-71d2-b3f0-256e1e49b1d1")
	if err != nil {
		t.Fatal(err)
	}
	var call map[string]jsontext.Value
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal(call["call_id"], &id); err != nil {
		t.Fatal(err)
	}
	if len(id) > 64 {
		t.Fatalf("invalid OpenAI call_id: %q", id)
	}
}
