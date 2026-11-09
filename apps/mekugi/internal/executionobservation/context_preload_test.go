package executionobservation

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/testsupport/moduletest"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestContextPreloadUsesNativeReloadAndRetainedResults(t *testing.T) {
	for _, kind := range []string{"assignment", "completion", "compact", "wait_agent"} {
		t.Run(kind, func(t *testing.T) { testContextReload(t, kind) })
	}
}

func TestUnsuccessfulCompactionDoesNotAttachReload(t *testing.T) {
	for _, name := range []string{"compact", "wait_agent"} {
		for _, state := range []string{"skipped", "failed", "canceled", "registered"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				p := newManagedMekugiProxy(t)
				output := mustTestJSON(t, map[string]any{"id": "root:checkpoint", "compaction": control.CompactionResult{State: state, Reload: `{"sources":[{"path":"selected.txt"}]}`}})
				request := codexresponses.Request{Fields: map[string]jsontext.Value{"input": mustTestJSON(t, []any{map[string]any{
					"type": "function_call_output", "namespace": "threadcontrol", "name": name, "output": string(output),
				}})}}
				reader, calls, events, err := p.preparePreloadInput(&request, "root", "root", codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn"}, false)
				if err != nil || len(reader) != 0 || len(calls) != 0 || len(events) != 0 {
					t.Fatalf("unsuccessful reset reloaded sources: reader=%s calls=%v events=%v err=%v", reader, calls, events, err)
				}
			})
		}
	}
}

func testContextReload(t *testing.T, kind string) {
	completion := kind == "completion"
	reset := kind == "compact" || kind == "wait_agent"
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	session, err := p.Retained.Store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
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
	if completion || reset {
		preload = control.ContextPreload{}
	}
	if err := service.AdmitRoot(t.Context(), controlSession, &control.Member{Thread: "root", Session: "routing", Task: "/root", AudienceAdmitted: true, Preload: preload, Turn: "rt", Generation: 1, Status: control.Working, HostRunning: true}); err != nil {
		t.Fatal(err)
	}
	p.ThreadControl, err = threadbinding.NewDriver(service)
	if err != nil {
		t.Fatal(err)
	}
	assignment := map[string]jsontext.Value{"type": mustTestJSON(t, "message"), "role": mustTestJSON(t, "assistant"), "id": mustTestJSON(t, itemID), "content": mustTestJSON(t, "assignment")}
	if completion {
		assignment["phase"] = mustTestJSON(t, "commentary")
		assignment["internal_chat_message_metadata_passthrough"] = mustTestJSON(t, map[string]any{"content_item_kinds": []string{"multi_agent.inter_agent_completion_message"}})
		assignment["content"] = mustTestJSON(t, []map[string]string{{"type": "output_text", "text": "Message Type: FINAL_ANSWER\nTask name: /root\nSender: /root/child\nOutcome: completed\nSource generation: 1\nSource turn: child-turn\nPayload:\nfinished\n" + compact.CompactMetadataStart + selection + compact.CompactMetadataEnd}})
	}
	if reset {
		id := control.Identity{Thread: "root", Session: "routing", Turn: "rt", Call: "checkpoint", Item: "cell"}
		_, err := service.RegisterCompaction(t.Context(), id, "", nil, nil, time.Now(), true, selection)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Execution(t.Context(), id.Thread, id.Turn, id.Item, "", true); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ClaimLocalStop(t.Context(), id.Thread, id.Turn, id.Item); err != nil {
			t.Fatal(err)
		}
		if err := service.HostCompleted(t.Context(), id.Thread, id.Turn, control.Completed, control.HostFailure{}); err != nil {
			t.Fatal(err)
		}
		effect, err := service.ClaimEffect(t.Context(), id.Thread, id.Call+":compact")
		if err != nil || effect == nil {
			t.Fatalf("compact effect: %+v, %v", effect, err)
		}
		if err := service.EffectAcknowledged(t.Context(), id.Thread, effect.ID, control.Receipt{}); err != nil {
			t.Fatal(err)
		}
		wakeEffect, err := service.PrepareWake(t.Context(), id.Thread, time.Now())
		if err != nil || wakeEffect == nil {
			t.Fatalf("wake: %+v, %v", wakeEffect, err)
		}
		claimed, err := service.ClaimEffect(t.Context(), id.Thread, wakeEffect.ID)
		if err != nil || claimed == nil {
			t.Fatalf("claimed wake: %+v, %v", claimed, err)
		}
		wake := control.NativeInput{ID: claimed.ID, Wake: claimed.Wake}
		itemID = control.MessageItemID(claimed.ID)
		assignment = map[string]jsontext.Value{"type": mustTestJSON(t, "function_call_output"), "namespace": mustTestJSON(t, "threadcontrol"), "name": mustTestJSON(t, kind), "output": mustTestJSON(t, wake.RetainedWaitOutput())}
	}
	meta := codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn"}
	var reader map[string]jsontext.Value
	modes := []string{"turn", "prewarm", "execution-free", "compacted", "foreign", "mismatched-thread", "invalid-identity"}
	if kind == "assignment" {
		modes = append(modes, "non-assistant", "unrelated-item")
	}
	if completion {
		modes = append(modes, "failed", "forged", "invalid-selection")
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			items := []map[string]jsontext.Value{maps.Clone(assignment)}
			if mode == "non-assistant" {
				items[0]["role"] = mustTestJSON(t, "user")
			}
			if mode == "unrelated-item" {
				items[0]["id"] = mustTestJSON(t, control.MessageItemID("unrelated"))
			}
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
			if mode == "invalid-identity" {
				metadata.ActivityIdentityInvalid = true
			}
			pending, _, supplements, err := p.preparePreloadInput(&request, identity, "root", metadata, mode == "prewarm")
			if mode == "foreign" || mode == "mismatched-thread" || mode == "invalid-identity" {
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
	} else {
		dir := t.TempDir()
		for name, body := range map[string]string{"selected.txt": "complete body\n@nested.txt", "nested.txt": "nested retained body\n@missing-nested", "mcat": "#!/bin/sh\nshift\nexec cat -- \"$@\"\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		captured, err := compact.ReadSources(t.Context(), `{"sources":[{"path":"selected.txt"}]}`, dir, os.Environ(), compact.ChangeReader{})
		if err != nil {
			t.Fatal(err)
		}
		sources[0] = captured.Sources[0]
		if err := os.WriteFile(filepath.Join(dir, "nested.txt"), []byte("changed nested source"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := compact.Snapshot{Selection: selection, Sources: sources}
	if completion {
		snapshot.Completion = &sources[0]
		snapshot.Sources = nil
	}
	result := compactTestSnapshot(t, p, snapshot)
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
	if !completion && (!bytes.Contains(visible, []byte("nested retained body")) || !bytes.Contains(visible, []byte("missing-nested")) || bytes.Contains(visible, []byte("changed nested source"))) {
		t.Fatalf("nested preload/reload lost retained bytes or errors: %s", visible)
	}
	if !bytes.Contains(visible, []byte("complete body")) || !bytes.Contains(visible, []byte("permission denied")) || !bytes.Contains(visible, []byte("composed retained diff")) || bytes.Contains(visible, []byte(compact.SnapshotPrefix)) || !bytes.Equal(request.OriginalField("input"), mustTestJSON(t, items)) {
		t.Fatalf("lost preload context or original history: %s", visible)
	}
	if completion {
		mixed := snapshot
		mixed.Sources = append(sources, sources[0])
		mixedItems := append([]map[string]jsontext.Value{}, items...)
		mixedItems[2] = map[string]jsontext.Value{"type": mustTestJSON(t, "custom_tool_call_output"), "call_id": reader["call_id"], "output": mustTestJSON(t, compactTestSnapshot(t, p, mixed))}
		invalid, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": mixedItems, "tools": []any{}}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.prepareModelRequest(t.Context(), &invalid, "routing", "root", meta, true, false); err == nil {
			t.Fatal("mixed completion/source snapshot accepted")
		}
		other := maps.Clone(assignment)
		otherID := control.MessageItemID("second-child:final")
		other["id"] = mustTestJSON(t, otherID)
		raw, _, err := compactReaderCall(compact.Record{Reference: otherID, Selection: selection}, nil, "root")
		if err != nil {
			t.Fatal(err)
		}
		var otherReader map[string]jsontext.Value
		if err := json.Unmarshal(raw, &otherReader); err != nil {
			t.Fatal(err)
		}
		multiple := append(append([]map[string]jsontext.Value{}, items...), other, otherReader, map[string]jsontext.Value{"type": mustTestJSON(t, "custom_tool_call_output"), "call_id": otherReader["call_id"], "output": mustTestJSON(t, result)})
		for range 2 {
			repeated, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": multiple, "tools": testExecResponsesTools()}))
			if err != nil {
				t.Fatal(err)
			}
			pending, _, _, err := p.preparePreloadInput(&repeated, "routing", "root", meta, false)
			if err != nil || len(pending) != 0 {
				t.Fatalf("completed readers reacquired: %s, %v", pending, err)
			}
			repeated.Fields["tools"] = mustTestJSON(t, []any{})
			if _, err := p.prepareModelRequest(t.Context(), &repeated, "routing", "root", meta, true, false); err != nil {
				t.Fatal(err)
			}
			if bytes.Count(repeated.Fields["input"], []byte("composed retained diff")) != 2 {
				t.Fatal("multiple completion attachments duplicated or lost")
			}
		}
		for range 2 {
			ref, err := moduletest.StoreCompact(t.Context(), p.Retained, "root", "completed handoffs retained by summary", "")
			if err != nil {
				t.Fatal(err)
			}
			compacted, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{map[string]string{"type": "compaction", "encrypted_content": ref}}, "tools": []any{}}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.prepareModelRequest(t.Context(), &compacted, "routing", "root", meta, true, false); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(compacted.Fields["input"], []byte("composed retained diff")) {
				t.Fatal("compaction replayed retired completion attachments")
			}
		}
	}
	// Inherited completed evidence does not depend on a surviving logical tree.
	p.ThreadControl = nil
	for _, mode := range []string{"completed", "yielded", "foreign", "changed-program"} {
		t.Run("ordinary-fork-"+mode, func(t *testing.T) {
			fork := "fork-" + mode
			ancestor := "root"
			if mode == "foreign" {
				ancestor = ""
			}
			if err := p.Retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
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
			forkRequest, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": inherited, "tools": testExecResponsesTools()}))
			if err != nil {
				t.Fatal(err)
			}
			forkMeta := meta
			forkMeta.ThreadID, forkMeta.ForkedFromThreadID = fork, ancestor
			transform, err := p.prepareModelRequest(t.Context(), &forkRequest, "fork-routing", fork, forkMeta, true, false)
			if mode == "foreign" || mode == "changed-program" {
				if err == nil {
					t.Fatal("untrusted inherited snapshot accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer transform.Close()
			if len(transform.CompactReload()) != 0 {
				t.Fatal("ordinary fork reacquired completed inherited sources")
			}
			visible := forkRequest.Fields["input"]
			if !bytes.Contains(visible, []byte("complete body")) || !bytes.Contains(visible, []byte("permission denied")) || !bytes.Contains(visible, []byte("composed retained diff")) || bytes.Contains(visible, []byte(compact.SnapshotPrefix)) || bytes.Contains(visible, []byte("Script running")) {
				t.Fatalf("ordinary fork lost inherited preload context: %s", visible)
			}
		})
	}
}
