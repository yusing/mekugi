package launch

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestWebUIBrowserRosterSemantics(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_WEBUI_BROWSER") != "1" {
		t.Skip("set MEKUGI_TEST_WEBUI_BROWSER=1 after make webui")
	}
	t.Setenv("AGENT_BROWSER_ENGINE", "chrome")
	browser := browserFixtureCommand(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	page := httptest.NewServer(webui.Handler())
	defer page.Close()
	token := strings.Repeat("e", 64)
	machine := newWebUIBrowserMachine(t, ctx, page.URL, "Roster", token)
	hook := activity.ItemContent{Kind: "hook", Text: "Stop · builtin", Details: &activity.ItemDetails{Hook: &activity.HookDetails{Status: "failed", HasError: true}}}
	frame := sessionapi.Frame{
		Session: sessionapi.Session{Thread: "same-thread", Caller: "/root", Title: "Roster thread", Workspace: machine.workdir},
		Input:   &sessionapi.Input{},
		Agents: &activity.Publication{Roster: []activity.AgentRow{
			{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root", Thread: "same-thread"}}},
			{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/worker", Thread: "cold", Responding: true}, Status: "responding", Summary: []activity.Entry{{Facts: hook.Facts, Values: hook.Values()}}}},
		}},
		Members: []threadcontrol.Participant{{Thread: "same-thread", Task: "/root"}, {Thread: "cold", Parent: "same-thread", Task: "/root/worker", Status: threadcontrol.Working}},
	}
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	identity, err := exec.CommandContext(ctx, browser, "session", "id", "--scope", "worktree", "--prefix", "webui-roster").CombinedOutput()
	if err != nil {
		t.Fatalf("browser identity: %v %s", err, identity)
	}
	name := strings.TrimSpace(string(identity))
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, browser, "--session", name, "close").Run()
	}()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, browser, append([]string{"--session", name}, args...)...).CombinedOutput()
		if err != nil {
			state, _ := exec.CommandContext(ctx, browser, "--session", name, "get", "text", "body").CombinedOutput()
			t.Fatalf("browser %s: %v %s\n%s", args[0], err, out, state)
		}
		return string(out)
	}
	run("open", page.URL)
	run("set", "viewport", "1280", "900")
	run("find", "label", "Machine name", "fill", "Roster")
	run("find", "label", "Connector URL", "fill", machine.remote.URL)
	run("find", "label", "Access token", "fill", token)
	run("find", "role", "button", "click", "--name", "Connect", "--exact")
	run("wait", "--text", "Roster thread")
	run("find", "text", "Roster thread", "click", "--exact")
	for _, theme := range []string{"light", "dark"} {
		run("set", "media", theme)
		for _, width := range []string{"1280", "390"} {
			run("set", "viewport", width, "900")
			run("eval", `if (!document.querySelector('#panel-content')) document.querySelector('button[aria-label^="Agents ("]').click()`)
			run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('#panel-content button')].find(row => row.textContent.includes('Hook Failed')); const verb = row?.querySelector('.font-medium.text-negative'); if (!row || !verb) return false; const rect = row.getBoundingClientRect(); return row.textContent.includes('Stop') && getComputedStyle(verb).color !== getComputedStyle(verb.parentElement).color && rect.left >= 0 && rect.right <= innerWidth; })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "roster-hook-"+theme+"-"+width+".png"))
			}
		}
	}
	// Repeated updates replace the roster headline while retaining transcript history.
	source := &sessionpresentation.ActivitySource{Model: new(activity.Model[sessionpresentation.ActivityEntry, sessionpresentation.ActivityAgent]), Audience: sessionpresentation.ChildAudience}
	source.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", Thread: "cold", AgentLifecycle: activity.AgentLifecycle{Responding: true}}}})
	for i, step := range []struct{ item, turn, text, phase, want string }{
		{"one", "first", "**Inspecting**", "summary", "Inspecting"},
		{"one", "first", "**Inspecting**\n\nDetails.\n\n**Validating**", "summary", "Validating"},
		{"two", "first", "**Finishing**", "summary", "Finishing"},
		{"two", "first", "**Finishing**", "item/completed", "Finishing"},
		{"three", "fresh", "**Fresh work**", "summary", "Fresh work"},
	} {
		source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: uint64(i + 1), Agent: "/root/worker", Kind: "reasoning", CallID: step.item, Text: step.text,
			Native: &sessionpresentation.NativeItem{Thread: "cold", Turn: step.turn, Item: step.item, Phase: step.phase, Live: true}}}})
		frame.Agents = &activity.Publication{Roster: append([]activity.AgentRow{frame.Agents.Roster[0]}, source.Publication(new(sessionpresentation.Model), nil).Roster...)}
		if err := machine.publishFrame(frame); err != nil {
			t.Fatal(err)
		}
		run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('#panel-content button')].find(row => row.textContent.includes('worker')); return row?.textContent.includes('`+step.want+`') && ['Inspecting','Validating','Finishing','Fresh work'].filter(label => row.textContent.includes(label)).length === 1; })()`)
		if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
			run("screenshot", filepath.Join(artifacts, "roster-reasoning-"+step.want+"-"+strings.ReplaceAll(step.phase, "/", "-")+".png"))
		}
		if step.phase == "item/completed" {
			source.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", Thread: "cold", AgentLifecycle: activity.AgentLifecycle{Final: true}}}})
			frame.Agents = &activity.Publication{Roster: append([]activity.AgentRow{frame.Agents.Roster[0]}, source.Publication(new(sessionpresentation.Model), nil).Roster...)}
			frame.Members[1].Status = threadcontrol.Completed
			if err := machine.publishFrame(frame); err != nil {
				t.Fatal(err)
			}
			run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('#panel-content button')].find(row => row.textContent.includes('worker')); return row?.textContent.includes('Done') && !row.textContent.includes('Finishing'); })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "roster-reasoning-settled.png"))
			}
			source.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/worker", Thread: "cold", AgentLifecycle: activity.AgentLifecycle{Responding: true}}}})
			frame.Members[1].Status = threadcontrol.Working
		}
	}
	// Cold restoration keeps the admitted member without an attached child feed.
	frame.Agents = &activity.Publication{Roster: frame.Agents.Roster[:1]}
	frame.Members[1].Status = threadcontrol.Paused
	if err := machine.publishFrame(frame); err != nil {
		t.Fatal(err)
	}
	run("wait", "--fn", `(() => { const row = [...document.querySelectorAll('#panel-content button')].find(row => row.textContent.includes('worker')); return row?.textContent.includes('Paused') && !row.textContent.includes('Idle') && !row.querySelector('[title="Time spent working"]'); })()`)

	// Retry notifications use the same producer and publication as the terminal.
	u, _ := newActiveWorkTestUI(t)
	u.startSession("same-thread", u.attached.cwd)
	u.Thread = "same-thread"
	u.attached.presentation.Attach(sessionpresentation.Thread{ID: "same-thread", Caller: "/root"}, false)
	observed := time.Now()
	u.clock = func() time.Time { return observed }
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	for _, thread := range []string{"same-thread", "child"} {
		appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": "retry-turn"}})
		appServerTestNotify(t, u, "error", map[string]any{"threadId": thread, "turnId": "retry-turn", "willRetry": true,
			"error": map[string]any{"message": "Reconnecting... 2/5", "additionalDetails": "Server overloaded"}})
	}
	publishRetry := func() {
		t.Helper()
		u.testMainFrame(100, 8, 0)
		agents := u.attached.childSource.Publication(u.attached.presentation, u.attached.source)
		if u.Turn != "" {
			for _, row := range agents.Roster {
				if row.Agent.Thread == "same-thread" && (!row.Agent.Waiting || row.Agent.Wait == nil) {
					t.Fatalf("Main retry missing before browser publication: %+v", row.Agent)
				}
			}
		}
		state := threadcontrol.Working
		if u.Turn == "" {
			state = threadcontrol.Interrupted
		}
		if err := machine.publishFrame(sessionapi.Frame{Session: frame.Session, Input: &sessionapi.Input{Stoppable: true},
			Members: []threadcontrol.Participant{{Thread: "same-thread", Task: "/root", Status: state}, {Thread: "child", Parent: "same-thread", Task: "/root/worker", Status: state}},
			Status:  u.attached.presentation.View().Status.Session,
			Main:    u.attached.source.Publication(u.attached.presentation, nil), Agents: agents}); err != nil {
			t.Fatal(err)
		}
	}
	publishRetry()
	for _, width := range []string{"1280", "390"} {
		run("set", "viewport", width, "900")
		run("eval", `if (!document.querySelector('#panel-content')) document.querySelector('button[aria-label^="Agents ("]').click()`)
		for _, theme := range []string{"light", "dark"} {
			run("set", "media", theme)
			run("wait", "--fn", `!!document.querySelector('[title="Reconnecting... 2/5 · Server overloaded"]')`)
			run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('#panel-content button')].filter(row => row.textContent.includes('Reconnecting... 2/5')); return rows.length >= 2 && rows.every(row => { const r = row.getBoundingClientRect(); return row.textContent.includes('Server overloaded') && r.left >= 0 && r.right <= innerWidth; }); })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "roster-retry-"+theme+"-"+width+".png"))
			}
		}
	}
	for _, thread := range []string{"same-thread", "child"} {
		appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "retry-turn", "status": "interrupted"}})
	}
	publishRetry()
	run("wait", "--fn", `!document.querySelector('#panel-content').textContent.includes('Reconnecting')`)

	// Responses and native SDK producers share the same server-pushed roster.
	generationInputs := new(sessioncontrol.Observations)
	u.attached.session.ToolInput = generationInputs.ToolInput
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "same-thread", "turn": map[string]any{"id": "tool-turn"}})
	var generation sessioncontrol.Generation
	item := sessioncontrol.GenerationItem{ID: "tool-input"}
	generationInputs.SetToolInput(&generation, "", "same-thread", item, true)
	generationInputs.IdentifyToolInput(&generation, item, "exec", true, `await tools.apply_patch("*** Begin Patch`)
	native := newClaudeHookFixture(t).bridge
	nativeEvents := claudeGenerationEvents(t)
	for _, raw := range nativeEvents[:2] {
		if err := native.event(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	nativePublication := claudePublication{frame: sessionapi.Frame{Session: sessionapi.Session{Thread: native.info.Thread}}}
	publishGeneration := func() {
		t.Helper()
		applyObservedActivity(&u.attached, &u.Client, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
		u.testMainFrame(100, 8, 0)
		facts, _ := native.runtime.Metrics()
		root, members := native.execution.shared.Family("parent")
		member, _ := native.execution.shared.Member(native.info.Thread)
		nativePublication.update(root, members, member, nil, facts)
		var rows []activity.AgentRow
		for _, row := range u.attached.childSource.Publication(u.attached.presentation, u.attached.source).Roster {
			if row.Agent.Thread == "same-thread" {
				rows = append(rows, row)
			}
		}
		rows = append(rows, nativePublication.frame.Agents.Roster...)
		if err := machine.publishFrame(sessionapi.Frame{Session: frame.Session, Status: u.attached.presentation.View().Status.Session, Input: &sessionapi.Input{Stoppable: true},
			Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: &activity.Publication{Roster: rows},
			Members: []threadcontrol.Participant{{Thread: "same-thread", Task: "/root", Status: threadcontrol.Working},
				{Thread: native.info.Thread, Parent: "same-thread", Task: "/root/child", Status: threadcontrol.Working, NonAttachable: true}}}); err != nil {
			t.Fatal(err)
		}
	}
	publishGeneration()
	run("wait", "--fn", `!!document.querySelector('header [title="Emitting a patch"]')`)
	run("wait", "--fn", `(() => { const rows = document.querySelector('#panel-content').textContent; return rows.includes('Emitting a patch') && rows.includes('Spawning subagent') && !rows.includes('Spawning subagent worker'); })()`)
	if err := native.event(t.Context(), nativeEvents[2]); err != nil {
		t.Fatal(err)
	}
	publishGeneration()
	for _, theme := range []string{"light", "dark"} {
		run("set", "media", theme)
		for _, width := range []string{"1280", "390"} {
			run("set", "viewport", width, "900")
			run("eval", `if (!document.querySelector('#panel-content')) document.querySelector('button[aria-label^="Agents ("]').click()`)
			run("wait", "--fn", `(() => { const rows = [...document.querySelectorAll('#panel-content button')]; const spawning = rows.find(row => row.textContent.includes('Spawning subagent')); return rows.some(row => row.textContent.includes('Emitting a patch')) && spawning?.querySelector('code')?.textContent === 'worker' && rows.every(row => { const r = row.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; }); })()`)
			if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
				run("screenshot", filepath.Join(artifacts, "roster-generation-"+theme+"-"+width+".png"))
			}
		}
	}
	generationInputs.EndGeneration(&generation)
	for _, raw := range nativeEvents[3:5] {
		if err := native.event(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
	}
	publishGeneration()
	run("wait", "--fn", `!!document.querySelector('header [title="Working"]') && !document.querySelector('header [title="Emitting a patch"]')`)
	run("wait", "--fn", `!document.querySelector('#panel-content').textContent.includes('Emitting a patch') && !document.querySelector('#panel-content').textContent.includes('Spawning subagent')`)

	// Side and One-off keep Main's header status, but own the composer hint.
	raw, ok := machine.views.Load("same-thread")
	if !ok {
		t.Fatal("missing native attachment")
	}
	attachment := raw.(sessionapi.NativeRequest)
	m := u.attached.presentation
	u.Turn = ""
	m.Status.Set("Completed", false)
	side := sessionpresentation.Thread{ID: "side", Caller: "/root/side"}
	m.SetSide(side)
	if err := m.Display(sessionpresentation.DisplayIntent{Version: m.View().Version, Thread: side}); err != nil {
		t.Fatal(err)
	}
	u.attached.conversation.Side = &conversationmodel.SideAnswer{Status: "Working"}
	if err := machine.publishFrame(sessionapi.Frame{Session: frame.Session, Status: sessionpresentation.Label{Text: "Completed"}, Input: &sessionapi.Input{}}); err != nil {
		t.Fatal(err)
	}
	run("set", "viewport", "1280", "900")
	run("wait", "--fn", `!!document.querySelector('header [title="Completed"]')`)
	for _, oneOff := range []bool{false, true} {
		u.attached.conversation.Side.OneOff = oneOff
		generationInputs.SetToolInput(&generation, "", "side", item, true)
		generationInputs.IdentifyToolInput(&generation, item, "exec", true, `await tools.mcp__threadcontrol__spawn_agent({message:"long`)
		for _, want := range []string{"Spawning subagent", "Spawning subagent `worker`", "Working"} {
			if want == "Working" {
				generationInputs.EndGeneration(&generation)
			} else if want != "Spawning subagent" {
				generationInputs.IdentifyToolInput(&generation, item, "", false, `",task_name:"worker",more:`)
			}
			view := m.Publish(sessionpresentation.MainInputs{Control: &u.Live, Conversation: u.attached.conversation,
				Info: sessionpresentation.MainInfo{Workspace: machine.workdir}, Feedback: &u.attached.feedback, SideRunning: true, ToolInput: generationInputs.ToolInput}, sessionpresentation.Caption{})
			if err := machine.api.Native().Publish(attachment.Attachment, sessionapi.NativeFrame{View: view}); err != nil {
				t.Fatal(err)
			}
			run("wait", "--fn", `document.querySelector('#composer-hint')?.textContent.trim() === '`+want+`'`)
			if want == "Spawning subagent `worker`" && !oneOff {
				for _, width := range []string{"1280", "390"} {
					run("set", "viewport", width, "900")
					run("eval", `document.querySelector('aside[aria-label="Thread panel"] button[aria-label="Close panel"]')?.click()`)
					for _, theme := range []string{"dark", "light"} {
						run("set", "media", theme)
						run("wait", "--fn", `(() => { const r = document.querySelector('#composer-hint')?.getBoundingClientRect(); return r?.width > 0 && r.left >= 0 && r.right <= innerWidth; })()`)
						if artifacts := os.Getenv("MEKUGI_TEST_WEBUI_ARTIFACTS"); artifacts != "" {
							run("screenshot", filepath.Join(artifacts, "composer-side-generation-"+width+"-"+theme+".png"))
						}
					}
				}
			}
		}
	}
}
