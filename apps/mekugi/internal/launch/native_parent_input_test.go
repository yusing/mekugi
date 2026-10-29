package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Retain the real request producer's payloads, including the different initial
// injection and follow-up continuation carriers, for restoration and UI checks.
func parentInputUI(t *testing.T, sender string) (*appServerUI, []map[string]any) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.attached.presentation.Publish(sessionpresentation.MainInputs{Feedback: &u.attached.feedback, Conversation: u.attached.conversation, Info: sessionpresentation.MainInfo{Caller: sender + "/worker"}}, sessionpresentation.Caption{})
	wire := u.Host.Input.(*appServerTestInput)
	var records []map[string]any
	for i, body := range []string{"Initial parent assignment.", "Follow-up parent assignment.", "Parent message."} {
		input := control.NativeInput{Thread: "main", ID: body, Sender: sender, Recipient: sender + "/worker", Kind: "NEW_TASK", Message: body, Run: i == 1}
		if i == 2 {
			input.Kind = "MESSAGE"
		}
		done := make(chan error, 1)
		wire.Reset()
		go func() { _, err := u.Requests.Input(t.Context(), input); done <- err }()
		u.Requests.Dispatch(<-u.Calls())
		var sent struct {
			ID     jsontext.Value `json:"id"`
			Params struct {
				Items  []map[string]any                         `json:"items"`
				Output struct{ Namespace, Name, Output string } `json:"toolOutput"`
			} `json:"params"`
		}
		if err := json.Unmarshal(wire.Bytes(), &sent, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		request, _ := u.Requests.Lookup(string(sent.ID))
		if !u.Requests.CompleteCall(appserver.Message{ID: sent.ID, Result: []byte(`{"turn":{"id":"continued"}}`)}) {
			t.Fatal("missing correlated acknowledgement")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		delivery := request.NativeDelivery()
		if delivery == nil {
			t.Fatalf("confirmed parent input missing: %+v", input)
		}
		publishNativeDelivery(&u.attached, *delivery, time.Date(2026, 10, 1, 12, 0, i, 0, time.Local))
		var record map[string]any
		if i == 1 {
			record = rolloutRecord(i, "response_item", map[string]any{"type": "function_call_output", "namespace": sent.Params.Output.Namespace, "name": sent.Params.Output.Name, "output": sent.Params.Output.Output})
			record["metadata"] = map[string]any{"client_authored": true}
		} else {
			record = rolloutRecord(i, "response_item", sent.Params.Items[0])
		}
		records = append(records, record, record)
	}
	return u, records
}

func TestNativeParentInputLiveAndRestored(t *testing.T) {
	for _, sender := range []string{"/root", "/root/coordinator"} {
		t.Run(sender, func(t *testing.T) {
			live, records := parentInputUI(t, sender)
			restored := newAppServerSessionTestUI(t, live.attached.cwd)
			restored.attached.presentation.Publish(sessionpresentation.MainInputs{Feedback: &restored.attached.feedback, Conversation: restored.attached.conversation, Info: sessionpresentation.MainInfo{Caller: sender + "/worker"}}, sessionpresentation.Caption{})
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd, Path: writeTestRollout(t, "main", records...)}); err != nil {
				t.Fatal(err)
			}
			for _, u := range []*appServerUI{live, restored} {
				entries := u.attached.source.Entries
				if len(entries) != 3 {
					t.Fatalf("parent input count = %d", len(entries))
				}
				for i, entry := range entries {
					wantKind := "assignment"
					if i == 2 {
						wantKind = "reply"
					}
					if entry.Source.Kind != wantKind || entry.Source.Agent != sender || len(entry.Values) != 1 || entry.Values[0].From != sender || entry.Values[0].To != "Main" {
						t.Fatalf("parent attribution lost: %+v", entry)
					}
				}
				view := u.testActivity(u.shell.Transcript)
				rendered := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
				for _, body := range []string{"Initial parent assignment.", "Follow-up parent assignment.", "Parent message."} {
					if strings.Count(rendered, body) != 1 {
						t.Fatalf("input missing or duplicated:\n%s", rendered)
					}
				}
				if route := pathdisplay.AgentDisplayName(sender) + " → " + pathdisplay.AgentDisplayName(sender+"/worker"); !strings.Contains(rendered, route) || strings.Contains(rendered, "You") {
					t.Fatalf("parent input rendered as outgoing/human:\n%s", rendered)
				}
			}
		})
	}
}

func TestUISnapshotNativeParentInput(t *testing.T) {
	u, _ := parentInputUI(t, "/root")
	view := u.testActivity(u.shell.Transcript)
	rows := view.testRenderConversation(80).Lines
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-parent-input.txt", append(rows, "plain after parent input"), 80)
}

func TestNativeFollowupRestoreRequiresHostProvenance(t *testing.T) {
	_, records := parentInputUI(t, "/root")
	for _, name := range []string{"missing provenance", "tool result", "foreign namespace", "other continuation"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(records[2])
			var record map[string]any
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			payload := record["payload"].(map[string]any)
			switch name {
			case "missing provenance":
				delete(record, "metadata")
			case "tool result":
				payload["call_id"] = "model-call"
			case "foreign namespace":
				payload["namespace"] = "other"
			case "other continuation":
				payload["name"] = "wait_agent"
			}
			restored := readRestoredRollout(binding.ThreadInfo{ID: "main", Path: writeTestRollout(t, "main", record)}, "/root")
			if len(restored.events) != 0 {
				t.Fatalf("lookalike became parent input: %+v", restored.events)
			}
		})
	}
}

func TestNativeParentInputInheritedRoutes(t *testing.T) {
	_, records := parentInputUI(t, "/root")
	for _, record := range records {
		metadata, _ := record["metadata"].(map[string]any)
		if metadata == nil {
			metadata = make(map[string]any)
			record["metadata"] = metadata
		}
		metadata["inherited_user_message"] = true
	}
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.attached.presentation.Publish(sessionpresentation.MainInputs{Feedback: &u.attached.feedback, Conversation: u.attached.conversation, Info: sessionpresentation.MainInfo{Caller: "/root/worker/reviewer"}}, sessionpresentation.Caption{})
	if err := u.restoreContent(binding.ThreadInfo{ID: "main", Cwd: u.attached.cwd, Path: writeTestRollout(t, "main", records...)}); err != nil {
		t.Fatal(err)
	}
	if len(u.attached.source.Entries) != 3 {
		t.Fatal("inherited traffic missing")
	}
	for _, entry := range u.attached.source.Entries {
		if value := entry.Values[0]; value.From != "/root" || value.To != "/root/worker" {
			t.Fatalf("inherited route retargeted: %+v", value)
		}
	}
	view := u.testActivity(u.shell.Transcript)
	rendered := ansi.Strip(strings.Join(view.testRenderConversation(100).Lines, "\n"))
	if !strings.Contains(rendered, "main") || !strings.Contains(rendered, "worker") || strings.Contains(rendered, "reviewer") || strings.Contains(rendered, "←") {
		t.Fatalf("inherited traffic rendered as local: %s", rendered)
	}
}
