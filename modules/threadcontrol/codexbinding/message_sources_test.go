package codexbinding

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type messageSourceHost struct {
	*reconciliationHost
	reads int
	deny  bool
	peer  *control.Peer
}

func (h *messageSourceHost) Runtime(string) (control.Runtime, error) { return h, nil }
func (h *messageSourceHost) Resume(context.Context, string) (control.Runtime, error) {
	return h, nil
}
func (h *messageSourceHost) ReadMessageSources(ctx context.Context, member *control.Member, selection string) (string, error) {
	h.reads++
	if h.deny {
		return "", errors.New("recipient denied source read")
	}
	snapshot, err := compact.ReadSources(ctx, selection, member.Settings.Workspace, os.Environ(), compact.ChangeReader{})
	return snapshot.Text(), err
}
func (h *messageSourceHost) Peer(control.Identity, string) (control.Peer, error) {
	if h.peer == nil {
		return control.Peer{}, errors.New("no fixture peer")
	}
	return *h.peer, nil
}

func sourceFixture(t *testing.T) (*Driver, *messageSourceHost, history.Session, control.Identity, string) {
	t.Helper()
	bin := t.TempDir()
	// Message sources select whole files. The shared reader's own tests cover
	// the real mcat range, output-window and continuation contracts.
	if err := os.WriteFile(filepath.Join(bin, "mcat"), []byte("#!/bin/sh\n[ \"$1\" = -- ] && shift\nexec cat -- \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	driver, base, session, caller := reconciliationFixture(t)
	host := &messageSourceHost{reconciliationHost: base}
	driver.host = host
	child, _ := driver.control.Member("child")
	workspace := child.Settings.Workspace
	for name, body := range map[string]string{
		"source.txt": "FROZEN_CHILD_BYTES @nested.txt", "nested.txt": "NESTED_CHILD_BYTES",
		"with spaces.txt": "QUOTED_PATH_BYTES", "literal": "MUST_NOT_ATTACH", "AGENTS.md": "HOST_ONLY_BYTES",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	caller.Call = "referenced-message"
	return driver, host, session, caller, workspace
}

func TestAgentMessageSourceDeliveryAndRetry(t *testing.T) {
	for _, operation := range []string{"send_message", "followup_task", "btw"} {
		t.Run(operation, func(t *testing.T) {
			driver, host, session, caller, workspace := sourceFixture(t)
			const message = `Read @source.txt @source.txt @"with spaces.txt" @missing.txt \@literal user@example.com @AGENTS.md; markers @ @@ @@@`
			var questions []string
			host.ask = func(_ context.Context, member *control.Member, text string) (string, error) {
				if member.Thread != "child" {
					t.Fatal("wrong recipient", member.Thread)
				}
				questions = append(questions, text)
				return "answer", nil
			}
			args := map[string]any{"target": "agent:main/child", "message": message}
			name := operation
			if operation == "btw" {
				args["btw"], name = true, "send_message"
			}
			raw, _ := json.Marshal(args)
			before := len(host.inputs)
			if _, err := driver.controlAction(t.Context(), caller, raw, name); err != nil {
				t.Fatal(err)
			}
			var got string
			if operation == "btw" {
				got = questions[0]
				if len(host.inputs) != before {
					t.Fatal("side question delivered primary input")
				}
			} else {
				got = host.inputs[before].Message
				if operation == "send_message" && host.inputs[before].Run {
					t.Fatal("ordinary message started work")
				}
			}
			for _, required := range []string{message, "FROZEN_CHILD_BYTES", "NESTED_CHILD_BYTES", "QUOTED_PATH_BYTES", "missing.txt", "exit_code=1"} {
				if !strings.Contains(got, required) {
					t.Fatalf("delivery missing %q: %s", required, got)
				}
			}
			for _, excluded := range []string{"MUST_NOT_ATTACH", "HOST_ONLY_BYTES"} {
				if strings.Contains(got, excluded) {
					t.Fatal("literal/host-only path acquired", got)
				}
			}
			if strings.Count(got, "FROZEN_CHILD_BYTES") != 1 {
				t.Fatal("duplicate reference acquired twice", got)
			}
			if strings.Count(got, "exit_code=1") != 1 {
				t.Fatal("literal markers produced extra failed reads", got)
			}
			if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("LATER_EDIT"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := driver.controlAction(t.Context(), caller, raw, name); err != nil {
				t.Fatal(err)
			}
			if host.reads != 1 {
				t.Fatalf("retry reacquired sources: %d", host.reads)
			}
			if operation == "btw" && (len(questions) != 2 || questions[1] != got) {
				t.Fatal("side-question retry changed source bytes")
			}
			if operation != "btw" {
				effect, err := driver.control.Effect(caller.Thread, caller.Call)
				if err != nil || effect.Sources == "" || effect.Message != message || len(host.inputs) != before+1 {
					t.Fatalf("effect/retry: %+v %v", effect, err)
				}
				restored := control.New()
				if err := restored.Restore(t.Context(), session); err != nil {
					t.Fatal(err)
				}
				text, err := restored.MessageText("root", effect.Sources, message)
				if err != nil || text != got {
					t.Fatalf("cold snapshot changed: %s %v", text, err)
				}
			}
		})
	}
}

func TestAgentMessageSourcesLiteralAndDenied(t *testing.T) {
	driver, host, _, caller, _ := sourceFixture(t)
	const literal = "Mention \\@literal and user@example.com, not a read: @ @@ @@@\n@@ -1,2 +3,4 @@"
	raw, _ := json.Marshal(map[string]string{"target": "child", "message": literal})
	before := len(host.inputs)
	if _, err := driver.controlAction(t.Context(), caller, raw, "send_message"); err != nil {
		t.Fatal(err)
	}
	if host.reads != 0 || host.inputs[before].Message != literal {
		t.Fatal("literal-only message changed or acquired context")
	}
	caller.Call = "denied"
	host.deny = true
	raw, _ = json.Marshal(map[string]string{"target": "child", "message": "Read @source.txt"})
	if _, err := driver.controlAction(t.Context(), caller, raw, "send_message"); err != nil {
		t.Fatal(err)
	}
	got := host.inputs[len(host.inputs)-1].Message
	if !strings.Contains(got, "CONTENT NOT ATTACHED: recipient denied source read") || strings.Contains(got, "FROZEN_CHILD_BYTES") {
		t.Fatal("permission failure was hidden", got)
	}
}

func TestRejectedMessageActionDoesNotAcquireSources(t *testing.T) {
	driver, host, _, caller, _ := sourceFixture(t)
	_, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"agent:main","message":"Read @source.txt"}`), "followup_task")
	if err == nil || host.reads != 0 {
		t.Fatal("rejected root control acquired source files", host.reads, err)
	}
}

func TestSpawnMessageSourcesAndReconciliation(t *testing.T) {
	driver, host, _, caller, workspace := sourceFixture(t)
	host.createThread = "second"
	caller.Call = "spawn-references"
	before := len(host.inputs)
	_, err := driver.SpawnPrepared(t.Context(), caller, PreparedChild{TaskName: "second", Message: "Read @source.txt", Settings: control.Settings{Workspace: workspace}})
	if err != nil {
		t.Fatal(err)
	}
	if host.reads != 1 || !strings.Contains(host.inputs[before].Message, "FROZEN_CHILD_BYTES") || host.inputs[before+1].Message != "" || !host.inputs[before+1].Run {
		t.Fatalf("spawn did not capture exactly once before first inference: %+v", host.inputs[before:])
	}
	caller.Call = "uncertain-references"
	host.failure = &binding.DispatchError{Outcome: binding.Uncertain, Cause: errors.New("lost acknowledgement")}
	raw := []byte(`{"target":"child","message":"Read @source.txt"}`)
	if _, err := driver.controlAction(t.Context(), caller, raw, "send_message"); err == nil {
		t.Fatal("lost acknowledgement succeeded")
	}
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("LATER_EDIT"), 0600); err != nil {
		t.Fatal(err)
	}
	host.failure = nil
	if _, err := driver.controlAction(t.Context(), caller, raw, "send_message"); err != nil {
		t.Fatal(err)
	}
	if host.reads != 2 || len(host.confirmed) == 0 || !strings.Contains(host.confirmed[len(host.confirmed)-1].Message, "FROZEN_CHILD_BYTES") {
		t.Fatal("uncertain confirmation changed the accepted source snapshot", host.confirmed)
	}
}

func TestFullHistorySpawnMessageSources(t *testing.T) {
	driver, host, _, caller, workspace := sourceFixture(t)
	host.createThread = "history-child"
	caller.Call = "history-spawn-references"
	imported, err := driver.control.PublishImport(t.Context(), caller, []byte("retained native fork fixture"))
	if err != nil {
		t.Fatal(err)
	}
	before := len(host.inputs)
	_, err = driver.SpawnPrepared(t.Context(), caller, PreparedChild{TaskName: "history-child", FullHistory: true, Import: imported, Message: "Read @source.txt", Settings: control.Settings{Workspace: workspace}})
	if err != nil || host.reads != 1 || !strings.Contains(host.inputs[before].Message, "FROZEN_CHILD_BYTES") {
		t.Fatalf("full-history assignment lost source capture: %v", err)
	}
}

func TestPeerMessageSourcesUseRecipientAndFreeze(t *testing.T) {
	for _, btw := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "btw"}[btw], func(t *testing.T) {
			driver, sender, _, caller, _ := sourceFixture(t)
			retained, err := history.NewService(control.RecordKinds())
			if err != nil {
				t.Fatal(err)
			}
			store, err := retained.Open(t.Context(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "remote")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			workspace := t.TempDir()
			path := filepath.Join(workspace, "source.txt")
			if err := os.WriteFile(path, []byte("FROZEN_REMOTE_BYTES"), 0600); err != nil {
				t.Fatal(err)
			}
			service := control.New()
			if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: "remote", Session: "remote-session", Task: "/root", Turn: "remote-turn", Generation: 1, Status: control.Working, HostRunning: true, AudienceAdmitted: true, Settings: control.Settings{Workspace: workspace}}); err != nil {
				t.Fatal(err)
			}
			remote, err := NewDriver(service)
			if err != nil {
				t.Fatal(err)
			}
			receiver := &messageSourceHost{reconciliationHost: &reconciliationHost{}}
			if err := remote.Connect(receiver, receiver); err != nil {
				t.Fatal(err)
			}
			origin, err := driver.control.Origin(caller)
			if err != nil {
				t.Fatal(err)
			}
			target, err := service.RemoteTarget("remote")
			if err != nil {
				t.Fatal(err)
			}
			sender.peer = &control.Peer{Source: origin, Target: target, Control: service, Runtime: receiver, Deliver: remote.DispatchPending}
			var questions []string
			receiver.ask = func(_ context.Context, _ *control.Member, question string) (string, error) {
				questions = append(questions, question)
				return "answer", nil
			}
			raw, _ := json.Marshal(map[string]any{"target": "remote", "message": "Read @source.txt", "btw": btw})
			if _, err := driver.controlAction(t.Context(), caller, raw, "send_message"); err != nil {
				t.Fatal(err)
			}
			var got string
			if btw {
				got = questions[0]
				if len(receiver.inputs) != 0 {
					t.Fatal("peer side question delivered primary input")
				}
			} else {
				got = receiver.inputs[0].Message
				if receiver.inputs[0].Run {
					t.Fatal("peer message started work")
				}
			}
			if !strings.Contains(got, "FROZEN_REMOTE_BYTES") || strings.Contains(got, "FROZEN_CHILD_BYTES") || sender.reads != 0 {
				t.Fatal("peer sources did not use recipient workspace", got)
			}
			if err := os.WriteFile(path, []byte("LATER_REMOTE_EDIT"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := driver.controlAction(t.Context(), caller, raw, "send_message"); err != nil {
				t.Fatal(err)
			}
			if receiver.reads != 1 || btw && questions[1] != got || !btw && len(receiver.inputs) != 1 {
				t.Fatal("peer retry reread or duplicated delivery")
			}
		})
	}
}
