package launch

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/diffrender"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Exercise the actual selection, capture, publication and hydration producers;
// only the native host lifecycle events are supplied by this renderer fixture.
func compactReloadUI(t *testing.T, sources []compact.Source) (*appServerUI, string, func()) {
	t.Helper()
	workspace := t.TempDir()
	for i := range sources {
		sources[i].Path = filepath.Join(workspace, sources[i].Path)
		if sources[i].Skill == "missing" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(sources[i].Path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sources[i].Path, []byte("selected source body\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	selection := string(mustTestJSON(t, map[string]any{"sources": sources}))
	script := compact.CaptureCommand(selection)
	u := newAppServerSessionTestUI(t, workspace)
	u.Turn = "turn"
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	u.audience = p.activity
	p.activity.Observe("main", "", "/root", false)
	p.activity.Attach("main")
	owner := executionobservation.New(executionobservation.Config{Retained: p.retained})
	ref, err := owner.StoreCompact(t.Context(), "main", "Retained handover", selection)
	if err != nil {
		t.Fatal(err)
	}
	input := []any{map[string]any{"type": "compaction", "encrypted_content": ref}}
	metadata := codexresponses.TurnMetadata{ThreadID: "main", TurnID: "turn", RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: jsonv1.RawMessage(`{}`)}}
	prepare := func() jsontext.Value {
		t.Helper()
		request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "instructions": testBaseInstructions, "tools": testExecResponsesTools(), "input": input}))
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := p.prepareRequest(t.Context(), &request, "routing", "main", metadata, true)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Close()
		for _, entry := range takeNativeActivity(u.audience, "main") {
			entry.Seq = u.attached.source.LastSeq + 1
			u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
		}
		return prepared.(interface{ CompactReload() jsontext.Value }).CompactReload()
	}
	var call map[string]jsontext.Value
	if err := json.Unmarshal(prepare(), &call); err != nil {
		t.Fatal(err)
	}
	complete := func() {
		t.Helper()
		captured, err := compact.Capture(t.Context(), command.Request{Args: []string{selection}})
		if err != nil {
			t.Fatal(err)
		}
		ns, err := p.retained.Store.NamespaceForThread("main")
		if err != nil {
			t.Fatal(err)
		}
		store, err := p.recovered.AttachStore(p.retained.Store, ns, "main", recovery.DefaultLimits(), nil)
		if err != nil {
			t.Fatal(err)
		}
		refs, err := store.Reserve(t.Context(), captured.Delivery.ReferenceCount())
		if err != nil {
			t.Fatal(err)
		}
		delivery, err := captured.Delivery.Prepare(refs)
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range delivery.Recovery {
			if _, err := store.PublishAt(t.Context(), refs[output.ReferenceIndex], output.OmittedOutput, output.ExitCode); err != nil {
				t.Fatal(err)
			}
		}
		input = append(input, call, map[string]any{"type": "custom_tool_call_output", "call_id": call["call_id"], "output": "Script completed\nWall time 0.1 seconds\nOutput:\n" + delivery.Referenced.Stdout})
		if len(prepare()) != 0 {
			t.Fatal("completed capture was scheduled again")
		}
	}
	return u, script, complete
}

func TestUISnapshotCompactReloadAttachments(t *testing.T) {
	u, script, complete := compactReloadUI(t, []compact.Source{{Path: "guide.txt"}, {Path: "skills/commit/SKILL.md", Skill: "commit"}})
	item := binding.ThreadItem{ID: "capture", Type: "commandExecution", Command: script, Status: "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	item.Status, item.ExitCode = "completed", new(0)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	if got := approvalTestTranscript(t, u); strings.Contains(got, "auto reloaded") {
		t.Fatal("aggregate success confirmed sources before capture", got)
	}
	complete()
	awaitMain(t, u, "Attached skill commit (auto reloaded)")
	finishPacing(u.testActivity(u.shell.Transcript))
	rows := u.testActivity(u.shell.Transcript).RenderFeed(100, 24).Lines
	assertNativeUISnapshot(t, "compact-reload-attachments", rows)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/compact-reload-attachments-ansi.txt", append(rows, "plain after attachments"), 100)
}

func TestUISnapshotCompactReloadApprovalLifecycle(t *testing.T) {
	u, script, complete := compactReloadUI(t, []compact.Source{{Path: "SKILL.md", Skill: "commit"}})
	approvalTestCommand(t, u, 7, map[string]any{"command": script, "availableDecisions": []string{"accept", "decline"}})
	if got := approvalTestTranscript(t, u); !strings.Contains(got, "Attached skill") || !strings.Contains(got, "Approving") || strings.Contains(got, "auto reloaded") {
		t.Fatalf("pending attachment: %s", got)
	}
	assertNativeUISnapshot(t, "compact-reload-approval-pending", u.testActivity(u.shell.Transcript).RenderFeed(100, 24).Lines)
	recordApproval(&u.attached, &u.approvals, u.shell, u.review, &u.pendingEditReceipts, u.Thread, u.clock, u.approvals.calls()[0], "Approved")
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: script, Status: "completed", ExitCode: new(0)}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	complete()
	finishPacing(u.testActivity(u.shell.Transcript))
	if got := approvalTestTranscript(t, u); !strings.Contains(got, "auto reloaded") || !strings.Contains(got, "Approved") {
		t.Fatalf("completed attachment: %s", got)
	}
}

func TestCompactReloadLoadsOnlySuccessfulSkills(t *testing.T) {
	u, script, complete := compactReloadUI(t, []compact.Source{{Path: "skills/commit/SKILL.md", Skill: "commit"}, {Path: "skills/missing/SKILL.md", Skill: "missing"}})
	item := binding.ThreadItem{ID: "capture", Type: "commandExecution", Command: script, Status: "completed", ExitCode: new(0)}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	complete()
	var loaded []string
	for _, record := range u.attached.source.Entries {
		loaded = append(loaded, activity.SkillLoads(record)...)
	}
	if !slices.Contains(loaded, "commit") || slices.Contains(loaded, "missing") {
		t.Fatalf("captured source outcomes lost: %v", loaded)
	}
}
