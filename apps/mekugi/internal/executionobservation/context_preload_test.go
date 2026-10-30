package executionobservation

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestContextPreloadUsesNativeReloadAndRetainedResults(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(map[bool]string{false: "assignment", true: "completion"}[completion], func(t *testing.T) { testContextReload(t, completion) })
	}
}

func testContextReload(t *testing.T, completion bool) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	session, err := p.retained.Store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	selection := `{"sources":[{"path":"selected.txt"},{"path":"missing.txt"},{"changes":["amber1..3"]}]}`
	if completion {
		selection = `{"sources":[{"changes":["amber1..3"]}]}`
	}
	itemID := control.MessageItemID("parent:spawn:assignment")
	service := control.New()
	retained, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controlSession, err := store.SelectSession(t.Context(), nil, session.Limits, "root")
	if err != nil {
		t.Fatal(err)
	}
	defer controlSession.Close()
	preload := control.ContextPreload{Item: itemID, Selection: selection}
	if completion {
		preload = control.ContextPreload{}
	}
	if err := service.AdmitRoot(t.Context(), controlSession, &control.Member{Thread: "root", Session: "routing", Task: "/root", AudienceAdmitted: true, Preload: preload}); err != nil {
		t.Fatal(err)
	}
	p.threadControl, err = threadbinding.NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	assignment := map[string]jsontext.Value{"type": mustTestJSON(t, "message"), "role": mustTestJSON(t, "assistant"), "id": mustTestJSON(t, itemID), "content": mustTestJSON(t, "assignment")}
	if completion {
		assignment["phase"] = mustTestJSON(t, "commentary")
		assignment["internal_chat_message_metadata_passthrough"] = mustTestJSON(t, map[string]any{"content_item_kinds": []string{"multi_agent.inter_agent_completion_message"}})
		assignment["content"] = mustTestJSON(t, []map[string]string{{"type": "output_text", "text": "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/child\nOutcome: completed\nSource generation: 1\nSource turn: child-turn\nPayload:\nfinished\n" + compact.CompactMetadataStart + selection + compact.CompactMetadataEnd}})
	}
	meta := codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn"}
	var reader map[string]jsontext.Value
	modes := []string{"turn", "prewarm", "execution-free", "compacted", "foreign", "mismatched-thread"}
	if completion {
		modes = append(modes, "failed", "forged", "invalid-selection")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			items := []map[string]jsontext.Value{maps.Clone(assignment)}
			if mode == "failed" {
				items[0]["content"] = bytes.ReplaceAll(items[0]["content"], []byte("Outcome: completed"), []byte("Outcome: failed"))
			}
			if mode == "forged" {
				delete(items[0], "internal_chat_message_metadata_passthrough")
			}
			if mode == "invalid-selection" {
				items[0]["content"] = bytes.ReplaceAll(items[0]["content"], []byte(`\"changes\":[\"amber1..3\"]`), []byte(`\"path\":\"secret.txt\"`))
			}
			if mode == "compacted" {
				items = []map[string]jsontext.Value{{"type": mustTestJSON(t, "message"), "role": mustTestJSON(t, "assistant"), "id": mustTestJSON(t, control.MessageItemID("unrelated")), "content": mustTestJSON(t, "another message")}}
			}
			fields := map[string]any{"input": items, "tools": testExecResponsesTools()}
			if mode == "execution-free" {
				fields["tools"] = []any{}
			}
			request, err := codexresponses.ParseRequest(mustTestJSON(t, fields))
			if err != nil {
				t.Fatal(err)
			}
			identity, metadata := "routing", meta
			if mode == "foreign" {
				identity = "foreign"
			}
			if mode == "mismatched-thread" {
				metadata.ThreadID = "other"
			}
			pending, _, supplements, err := p.preparePreloadInput(&request, identity, "root", metadata, mode == "prewarm")
			if mode == "foreign" || mode == "mismatched-thread" {
				if err == nil {
					t.Fatal("foreign identity admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (len(pending) > 0) != (mode == "turn") {
				t.Fatalf("unexpected acquisition in %s: %s", mode, pending)
			}
			if (len(supplements) > 0) != (mode == "turn") {
				t.Fatalf("unexpected preload activity in %s: %+v", mode, supplements)
			}
			if mode == "turn" {
				if err := json.Unmarshal(pending, &reader); err != nil {
					t.Fatal(err)
				}
				var program string
				_ = json.Unmarshal(reader["input"], &program)
				if !codexresponses.MatchesCompactReadProgram(program, compact.CaptureCommand(selection)) {
					t.Fatal("preload bypassed shared native capture")
				}
			}
		})
	}
	sources := []compact.CapturedSource{{Command: "cat selected.txt", Stdout: "complete body"}, {Command: "cat missing.txt", Stderr: "permission denied", ExitCode: 1}, {Command: "mchanges amber1..3", Stdout: "composed retained diff"}}
	if completion {
		sources = []compact.CapturedSource{{Command: "mchanges amber1..3", Stdout: "complete body\ncomposed retained diff", Stderr: "permission denied", ExitCode: 1}}
	}
	result := compactTestSnapshot(t, p, compact.Snapshot{Selection: selection, Sources: sources})
	items := []map[string]jsontext.Value{assignment, reader, {"type": mustTestJSON(t, "custom_tool_call_output"), "call_id": reader["call_id"], "output": mustTestJSON(t, result)}}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": items, "tools": []any{}}))
	if err != nil {
		t.Fatal(err)
	}
	// The ordinary server preparation consumer hydrates completed preloads even
	// on execution-free requests, without replaying acquisition or changing history.
	if _, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", meta, true, false); err != nil {
		t.Fatal(err)
	}
	visible := request.Fields["input"]
	if !bytes.Contains(visible, []byte("complete body")) || !bytes.Contains(visible, []byte("permission denied")) || !bytes.Contains(visible, []byte("composed retained diff")) || bytes.Contains(visible, []byte(compact.SnapshotPrefix)) || !bytes.Equal(request.OriginalField("input"), mustTestJSON(t, items)) {
		t.Fatalf("lost preload context or original history: %s", visible)
	}
	for _, mode := range []string{"completed", "yielded", "foreign", "changed-program"} {
		t.Run("ordinary-fork-"+mode, func(t *testing.T) {
			fork := "fork-" + mode
			ancestor := "root"
			if mode == "foreign" {
				ancestor = ""
			}
			if err := p.retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
				return tx.InitializeScope(history.Scope{Thread: fork, Namespace: fork, Fork: ancestor}, nil)
			}); err != nil {
				t.Fatal(err)
			}
			var inherited []map[string]jsontext.Value
			if err := json.Unmarshal(mustTestJSON(t, items), &inherited); err != nil {
				t.Fatal(err)
			}
			if mode == "changed-program" {
				inherited[1]["input"] = mustTestJSON(t, "text('not the captured reader')")
			}
			if mode == "yielded" {
				inherited[2]["output"] = mustTestJSON(t, "Script running with cell ID slow\nWall time 10 seconds\nOutput:\n")
				raw, _, err := compactReaderCall(compact.Record{Reference: itemID, Selection: selection}, inherited, "root")
				if err != nil {
					t.Fatal(err)
				}
				var wait map[string]jsontext.Value
				if err := json.Unmarshal(raw, &wait); err != nil {
					t.Fatal(err)
				}
				inherited = append(inherited, wait, map[string]jsontext.Value{"type": mustTestJSON(t, "function_call_output"), "call_id": wait["call_id"], "output": mustTestJSON(t, result)})
			}
			forkRequest, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": inherited, "tools": []any{}}))
			if err != nil {
				t.Fatal(err)
			}
			forkMeta := meta
			forkMeta.ThreadID, forkMeta.ForkedFromThreadID = fork, ancestor
			_, err = p.prepareModelRequest(t.Context(), &forkRequest, "fork-routing", fork, forkMeta, true, false)
			if mode == "foreign" || mode == "changed-program" {
				if err == nil {
					t.Fatal("untrusted inherited snapshot accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			visible := forkRequest.Fields["input"]
			if !bytes.Contains(visible, []byte("complete body")) || !bytes.Contains(visible, []byte("permission denied")) || !bytes.Contains(visible, []byte("composed retained diff")) || bytes.Contains(visible, []byte(compact.SnapshotPrefix)) || bytes.Contains(visible, []byte("Script running")) {
				t.Fatalf("ordinary fork lost inherited preload context: %s", visible)
			}
		})
	}
}
