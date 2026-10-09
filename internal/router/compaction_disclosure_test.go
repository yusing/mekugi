package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUISnapshotAutomaticResetRolloutDisclosure(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "skills-mgr"), []byte("#!/bin/sh\n[ \"$1 $2\" = 'get --codex' ] || exit 1\nprintf 'Skill %s at reset.\\n' \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	history := []any{map[string]any{"role": "developer", "content": `<skills><skill name="demo" description="test"/><skill name="unused"/></skills>`}}
	for range 6 {
		history = append(history, map[string]any{"role": "assistant", "content": "Earlier response."}, map[string]any{"role": "user", "content": "Continue."})
	}
	history = append(history,
		map[string]any{"role": "user", "content": "Use $demo $selected $attached. Plain name unused does not select a skill."},
		map[string]any{"role": "user", "content": managedSkillReference("demo")},
		map[string]any{"role": "user", "content": managedSkillReference("selected")},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": encodeFileAttachments(frameComposerSkillFromPath("attached", "", "Original skill."))}}})
	item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread, history...)
	if recovery.Guidance == nil || len(recovery.Guidance.Commands) != 3 {
		t.Fatalf("user skills were not loaded after five rounds: %+v", recovery.Guidance)
	}
	for _, name := range []string{"demo", "selected", "attached"} {
		if !strings.Contains(recovery.Guidance.Text, "Skill "+name+" at reset.") {
			t.Fatalf("missing user skill %s: %s", name, recovery.Guidance.Text)
		}
	}
	request, headers := v2ContinuityRequest(t, workspace, thread, "", "", item)
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "skill-forward", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	var forwarded struct {
		Input []struct {
			Output string `json:"output"`
		} `json:"input"`
	}
	if len(provider.forwarded) != 1 || json.Unmarshal(provider.forwarded[0], &forwarded) != nil || len(forwarded.Input) < 3 || forwarded.Input[2].Output != recovery.Guidance.Text {
		t.Fatal("model did not receive the exact retained skill guidance")
	}
	turn := recovery.Turn
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "log", Text: new("Newer state must not replace original recovery")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "skills-mgr"), []byte("#!/bin/sh\nprintf 'Skill %s at later reset.\\n' \"$3\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, later := deliverV2ContinuityReset(t, reopenV2ContinuityProxy(t, proxy), workspace, thread, item)
	if later.Guidance == nil || !slices.Equal(later.Guidance.Commands, recovery.Guidance.Commands) || later.Guidance.Text != strings.ReplaceAll(recovery.Guidance.Text, " at reset.", " at later reset.") {
		t.Fatal("restart lost retained user skill references")
	}
	completed := func(item string) map[string]any {
		return map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": thread, "turn_id": turn, "item": map[string]any{"type": "ContextCompaction", "id": item}}}
	}
	info := appServerThreadInfo{ID: thread, Cwd: workspace, Path: writeTestRollout(t, thread,
		map[string]any{"type": "compacted", "payload": map[string]any{"compaction_response_id": "provider-response"}}, completed("provider-item"),
		map[string]any{"type": "compacted", "payload": map[string]any{"compaction_response_id": recovery.ResponseID}}, completed("journal-item"))}
	for _, restored := range []bool{true, false} {
		u := newAppServerSessionTestUI(t, workspace)
		u.view.clock = func() time.Time { return time.Date(2026, 9, 30, 20, 0, 0, 0, time.Local) }
		u.clock = u.view.clock
		u.thread, u.proxy = thread, proxy
		if restored {
			u.proxy = reopenV2ContinuityProxy(t, proxy)
		}
		u.session.start(thread, workspace)
		u.restoreContextUsage(u.session.agent("/root"), info)
		u.journal = &nativeJournalSink{workspace: workspace, thread: thread}
		items := []appServerItem{{ID: "provider-item", Type: "contextCompaction"}, {ID: "journal-item", Type: "contextCompaction"}}
		if restored {
			u.restoreHistory([]appServerHistoryTurn{{ID: turn, Status: "completed", Items: items}})
		} else {
			for _, item := range items {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": item})
			}
		}
		fixResetPresentationTime(u)
		feed := u.view.renderFeed(90, 20)
		row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool {
			b, ok := u.view.snippetBlock(s)
			return ok && b.Verb == "Journal recovery"
		})
		if row < 0 {
			t.Fatal("automatic journal reset has no recovery target")
		}
		shell := selectionTestUI(feed.lines...)
		shell.main = u
		u.view.feedTop, u.view.feedLeft, u.view.feedRight, u.view.feedRows = 1, 1, 90, len(feed.lines)
		u.view.feedSnippets = feed.snippets
		if !shell.selectionMouse(0, 2, row, false) || !shell.selectionMouse(0, 2, row, true) || shell.output == nil || shell.output.pages[0].Body != recovery.Text+"\n"+recovery.Guidance.Text {
			t.Fatal("automatic reset click lost the original recovery text")
		}
		// Render the retained guidance in the same scrollable dialog. Fixed
		// workspace text keeps wrapping independent of the temporary directory.
		shell.output.origins = nil
		shell.output.pages[0].Body = strings.ReplaceAll(shell.output.pages[0].Body, workspace, "/workspace")
		shell.output.layout(86)
		shell.output.top = shell.output.starts[len(shell.output.starts)-1]
		rows := make([]string, 28)
		shell.paintOutput(rows, 90, len(rows))
		assertNativeUISnapshot(t, "automatic-reset-rollout-disclosure", append(feed.lines, rows...))
		if text, _, _ := u.progress(items[0], "item/completed", thread, turn); text != "Context reset" {
			t.Fatal("provider item acquired journal provenance")
		}
	}
	if rolloutCompactionResponse(appServerThreadInfo{ID: "foreign", Path: info.Path}, turn, "journal-item") != "" || rolloutCompactionResponse(info, "foreign-turn", "journal-item") != "" {
		t.Fatal("rollout provenance crossed identity boundaries")
	}
	info.Path = writeTestRollout(t, thread, map[string]any{"type": "compacted", "payload": map[string]any{"compaction_response_id": recovery.ResponseID}})
	if rolloutCompactionResponse(info, turn, "journal-item") != "" {
		t.Fatal("legacy rollout acquired exact item provenance")
	}
	info.Path = writeTestRollout(t, thread, completed("previous-item"), completed("journal-item"))
	if rolloutCompactionResponse(info, turn, "journal-item") != "" {
		t.Fatal("ambiguous item acquired another item's installed response")
	}
}
