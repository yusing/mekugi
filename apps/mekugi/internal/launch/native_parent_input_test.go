package launch

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Parent-authored input is native attributed traffic, never human input.
func parentInputUI(t *testing.T, sender string) (*appServerUI, []map[string]any) {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.attached.threadInput = &threadInput{admitted: true, caller: sender + "/worker"}
	u.attached.presentation.Publish(sessionpresentation.MainInputs{Feedback: &u.attached.feedback, Conversation: u.attached.conversation, Info: sessionpresentation.MainInfo{Caller: sender + "/worker"}}, sessionpresentation.Caption{})
	var records []map[string]any
	for i, body := range []string{"Initial parent assignment.", "Follow-up parent assignment.", "Parent message."} {
		kind := "NEW_TASK"
		if i == 2 {
			kind = "MESSAGE"
		}
		record := nativeTrafficFixture(t, u, fmt.Sprint("amsg_", i), sender, sender+"/worker", kind, body, "", time.Date(2026, 10, 1, 12, 0, i, 0, time.Local))
		records = append(records, record, record)
	}
	return u, records
}

func TestNativeParentInputLiveAndRestored(t *testing.T) {
	for _, sender := range []string{"/root", "/root/coordinator"} {
		t.Run(sender, func(t *testing.T) {
			live, records := parentInputUI(t, sender)
			restored := newAppServerSessionTestUI(t, live.attached.cwd)
			restored.attached.threadInput = &threadInput{admitted: true, caller: sender + "/worker"}
			restored.attached.presentation.Publish(sessionpresentation.MainInputs{Feedback: &restored.attached.feedback, Conversation: restored.attached.conversation, Info: sessionpresentation.MainInfo{Caller: sender + "/worker"}}, sessionpresentation.Caption{})
			if err := restored.restoreContent(binding.ThreadInfo{ID: "main", Cwd: live.attached.cwd, Path: writeTestRollout(t, "main", records...)}); err != nil {
				t.Fatal(err)
			}
			for _, u := range []*appServerUI{live, restored} {
				entries := u.attached.source.Entries
				if len(entries) != 3 {
					t.Fatalf("parent input count = %d", len(entries))
				}
				summary := u.attached.source.SummaryEntries("Main", true)
				if len(summary) != 1 || summary[0].AssignmentText != "Follow-up parent assignment." {
					t.Fatalf("incoming follow-up missing from runtime activity: %+v", summary)
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
	for _, name := range []string{"ordinary message", "foreign author", "foreign recipient", "missing id"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(&records[2])
			var record map[string]any
			if err := json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			payload := record["payload"].(map[string]any)
			switch name {
			case "ordinary message":
				payload["type"], payload["role"] = "message", "assistant"
			case "foreign author":
				payload["author"] = "/root/other"
			case "foreign recipient":
				payload["recipient"] = "/root/other"
			case "missing id":
				delete(payload, "id")
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
