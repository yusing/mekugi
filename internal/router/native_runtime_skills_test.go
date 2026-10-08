package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/session"
)

// Exercise the real JSON-lines client/decoder and shared renderer without
// inference or native effects. The fixture only forwards supplied SDK frames.
func runtimeDecodedFrames(t *testing.T, u *appServerUI) func(string) {
	t.Helper()
	dir := t.TempDir()
	bridge := filepath.Join(dir, "frames.mjs")
	if err := os.WriteFile(bridge, []byte(`import {createInterface} from 'node:readline';
for await (const line of createInterface({input: process.stdin})) {
  process.stdout.write(JSON.parse(line).text + '\n');
  process.stdout.write('{"kind":"notice","text":"fixture frame complete"}\n');
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	client, err := claude.Start(t.Context(), node, bridge, claude.Config{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return func(frame string) {
		t.Helper()
		if err := client.Send(t.Context(), frame); err != nil {
			t.Fatal(err)
		}
		for event := range client.Events() {
			if event.Kind == "notice" && event.Text == "fixture frame complete" {
				return
			}
			if err := u.runtimeEvent(event); err != nil {
				t.Fatal(err)
			}
		}
		t.Fatal("fixture ended before decoding its frame")
	}
}

func TestUISnapshotNativeRuntimeConfirmedSkills(t *testing.T) {
	u, _ := runtimeTestUI(t)
	feed := runtimeDecodedFrames(t, u)
	tool := func(id, caller, name, input string) {
		feed(fmt.Sprintf(`{"kind":"event","event":{"type":"assistant","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}}`, caller, id, name, input))
	}
	result := func(id, caller, response string, failed bool) {
		feed(fmt.Sprintf(`{"kind":"event","event":{"type":"user","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"Native read receipt","is_error":%t}]},"tool_use_result":%s}}`, caller, id, failed, response))
	}
	counts := func(main, child []string) {
		t.Helper()
		for _, view := range []*liveActivityView{u.view, u.agents} {
			for owner, want := range map[string][]string{"Main": main, "/root/child": child} {
				got := slices.Clone(view.activeSkills()[owner])
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%s loaded skills = %v, want %v", owner, got, want)
				}
			}
		}
	}
	tool("skill", "", "Skill", `{"skill":"requested-alias"}`)
	counts(nil, nil)
	result("skill", "", `{"success":true,"commandName":"plugin:guide"}`, false)
	assertNativeUISnapshot(t, "native-runtime-confirmed-skill", u.view.renderFeed(80, 12).lines)
	tool("repeat", "", "Skill", `{"skill":"another-alias"}`)
	result("repeat", "", `{"success":true,"commandName":"plugin:guide"}`, false)
	tool("read", "", "Read", `{"file_path":"/work/testing/SKILL.md"}`)
	counts([]string{"plugin:guide"}, nil)
	result("read", "", `{}`, false)
	// Failure, incomplete canonical evidence and another tool's similarly
	// shaped receipt cannot turn a requested alias into a confirmed load.
	for i, response := range []string{`{"success":false,"commandName":"not-loaded"}`, `{}`, `{"success":true,"commandName":"failed"}`} {
		id := fmt.Sprint("unconfirmed-", i)
		tool(id, "", "Skill", `{"skill":"not-loaded"}`)
		result(id, "", response, i == 2)
	}
	tool("failed-read", "", "Read", `{"file_path":"/work/failed/SKILL.md"}`)
	result("failed-read", "", `{}`, true)
	tool("not-skill", "", "Grep", `{"pattern":"needle"}`)
	result("not-skill", "", `{"success":true,"commandName":"not-loaded"}`, false)
	tool("ambiguous", "", "Skill", `{"skill":"not-loaded"}`)
	feed(`{"kind":"event","event":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ambiguous","content":"first"},{"type":"tool_result","tool_use_id":"not-skill","content":"second"}]},"tool_use_result":{"success":true,"commandName":"ambiguous-name"}}}`)
	counts([]string{"plugin:guide", "testing"}, nil)

	tool("child-load", "parent-tool", "Skill", `{"skill":"child-alias"}`)
	result("child-load", "parent-tool", `{"success":true,"commandName":"child-guide"}`, false)
	// Retire entries before child metadata, then bind the real parent-tool ID.
	for range liveActivityFeedLimit {
		for _, view := range []*liveActivityView{u.view, u.agents} {
			view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: view.lastSeq + 1, Agent: "Main", Kind: "text", Text: "Later note"}}})
		}
	}
	feed(`{"kind":"event","event":{"type":"system","subtype":"task_started","task_id":"child","task_type":"local_agent","tool_use_id":"parent-tool","description":"Read guides"}}`)
	counts([]string{"plugin:guide", "testing"}, []string{"child-guide"})
	feed(`{"kind":"event","event":{"type":"system","subtype":"task_progress","task_id":"child","tool_use_id":"parent-tool"}}`)
	counts([]string{"plugin:guide", "testing"}, []string{"child-guide"})
	for _, target := range []struct {
		view *liveActivityView
		name string
		body string
	}{{u.view, "Main", "- plugin:guide\n- testing"}, {u.agents, "/root/child", "- child-guide"}} {
		if !u.shell.openSkills(target.view, target.name) || u.shell.output.pages[0].Body != target.body {
			t.Fatal("native loaded skills did not open their shared name dialog")
		}
	}
	feed(`{"kind":"event","event":{"type":"system","subtype":"compact_boundary","uuid":"main-compact"}}`)
	counts(nil, []string{"child-guide"})
	tool("after-compact", "", "Read", `{"file_path":"/work/fresh/SKILL.md"}`)
	result("after-compact", "", `{}`, false)
	feed(`{"kind":"event","event":{"type":"conversation_reset","uuid":"main-reset"}}`)
	counts(nil, []string{"child-guide"})
	tool("new-main", "", "Read", `{"file_path":"/work/fresh/SKILL.md"}`)
	result("new-main", "", `{}`, false)
	feed(`{"kind":"reset","id":"journal-reset","sessionID":"new-native-session"}`)
	counts(nil, []string{"child-guide"})
}

func TestUISnapshotNativeRuntimeSavedSkills(t *testing.T) {
	u, _ := runtimeTestUI(t)
	feed := runtimeDecodedFrames(t, u)
	scan := func(agent, phase string, failed bool) {
		feed(fmt.Sprintf(`{"kind":"skill_history","agentID":%q,"phase":%q,"failed":%t}`, agent, phase, failed))
	}
	load := func(agent, id, name, input, receipt string, failed bool) {
		feed(fmt.Sprintf(`{"kind":"skill_history","agentID":%q,"phase":"message","event":{"type":"assistant","uuid":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}}`, agent, id, id, name, input))
		feed(fmt.Sprintf(`{"kind":"skill_history","agentID":%q,"phase":"message","event":{"type":"user","uuid":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":%q,"is_error":%t}]}}}`, agent, id+"-result", id, receipt, failed))
	}
	counts := func(main, child []string) {
		t.Helper()
		for _, v := range []*liveActivityView{u.view, u.agents} {
			for owner, want := range map[string][]string{"Main": main, "/root/child": child} {
				got := slices.Clone(v.activeSkills()[owner])
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Fatalf("%s saved loads = %v, want %v", owner, got, want)
				}
			}
		}
	}
	scan("", "start", false)
	load("", "old", "Read", `{"file_path":"/work/old/SKILL.md"}`, "old body", false)
	feed(`{"kind":"skill_history","phase":"message","event":{"type":"user","uuid":"summary","isCompactSummary":true,"message":{"content":[]}}}`)
	load("", "canonical", "Skill", `{"skill":"alias"}`, "Launching skill: plugin:guide", false)
	load("", "read", "Read", `{"file_path":"/work/testing/SKILL.md"}`, "skill body", false)
	load("", "failed", "Read", `{"file_path":"/work/failed/SKILL.md"}`, "denied", true)
	counts(nil, nil) // A partial scan cannot publish a count.
	scan("", "done", false)
	feed(`{"kind":"saved_agent","id":"child","callers":["parent-tool"]}`)
	scan("child", "start", false)
	load("child", "child-read", "Read", `{"file_path":"/work/child-guide/SKILL.md"}`, "child body", false)
	scan("child", "done", false)
	counts([]string{"plugin:guide", "testing"}, []string{"child-guide"})
	// Lazy display and retained rows cannot add a requested alias to the scan.
	feed(`{"kind":"history","event":{"type":"assistant","uuid":"display","message":{"content":[{"type":"tool_use","id":"display-only","name":"Read","input":{"file_path":"/work/not-current/SKILL.md"}}]}}}`)
	feed(`{"kind":"history","event":{"type":"user","uuid":"display-result","message":{"content":[{"type":"tool_result","tool_use_id":"display-only","content":"saved"}]}}}`)
	counts([]string{"plugin:guide", "testing"}, []string{"child-guide"})
	paintTitle := func(name string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			screen := vt.NewEmulator(120, 28)
			defer screen.Close()
			if err := u.paint(screen, 120, 28); err != nil {
				t.Fatal(err)
			}
			assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n")[:1])
		})
	}
	paintTitle("native-runtime-saved-skills")
	scan("", "start", false)
	load("", "unknown", "Skill", `{"skill":"alias"}`, "Unrecognized native receipt", false)
	scan("", "done", false)
	counts(nil, []string{"child-guide"})
	if u.notice == "" {
		t.Fatal("incomplete canonical receipt did not show a notice")
	}
	paintTitle("native-runtime-unknown-saved-skills")
	scan("child", "start", false)
	load("child", "partial", "Read", `{"file_path":"/work/partial/SKILL.md"}`, "child body", false)
	scan("child", "done", true)
	counts(nil, nil)
	feed(`{"kind":"event","event":{"type":"system","subtype":"compact_boundary","uuid":"live-boundary"}}`)
	u.runtimeEntry(session.Event{Kind: "tool", ID: "live", Role: "Read", Text: `{"file_path":"/work/new/SKILL.md"}`})
	u.runtimeEntry(session.Event{Kind: "tool_result", ID: "live", Text: "native read"})
	counts([]string{"new"}, nil)
}
