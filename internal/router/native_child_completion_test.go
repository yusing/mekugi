package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

const nativeChildOriginal = "The complete provider answer.\n\n- First finding\n- Second finding\n\n```go\nverify()\n```\n\nEnd of original answer."

type nativeChildCompletionFixture struct {
	proxy            *mekugiProxy
	workspace        string
	source, enriched appServerItem
}

// Produce the replacement through the real child response transform, not an
// invented router message prefix or a manually populated UI annotation.
func newNativeChildCompletionFixture(t *testing.T, stream bool) nativeChildCompletionFixture {
	t.Helper()
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	workspace := t.TempDir()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": "Review the change."}},
		"tools": []any{map[string]any{"type": "function", "name": "lookup"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	child, err := p.prepareRequest(t.Context(), &request, "session", "child", codexTurnMetadata{
		RequestKind: "turn", ThreadID: "child", TurnID: "child-turn", ParentThreadID: "main", AgentName: "/root/worker", SubagentKind: "thread_spawn",
		Directories: map[string]jsonv1.RawMessage{workspace: nil},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(child.Close)
	if _, err := p.journals.apply(t.Context(), p.replayStore, workspace, "child", "", []journalMutation{{Op: "log", Text: new("Focused validation passed.")}}); err != nil {
		t.Fatal(err)
	}
	source := map[string]any{"id": "provider-answer", "type": "message", "role": "assistant", "phase": "final_answer", "content": []any{map[string]any{"type": "output_text", "text": nativeChildOriginal}}}
	response := map[string]any{"id": "child-response", "status": "completed", "output": []any{source}}
	type outputItem struct {
		ID      string `json:"id"`
		Phase   string `json:"phase"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	var items []outputItem
	if stream {
		for _, event := range []any{
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": source},
			map[string]any{"type": "response.completed", "response": response},
		} {
			events, err := child.TransformSSE(mustTestJSON(t, event))
			if err != nil {
				t.Fatal(err)
			}
			for _, wire := range events {
				child.Delivered(wire)
				var envelope struct {
					Type string     `json:"type"`
					Item outputItem `json:"item"`
				}
				if err := json.Unmarshal(wire, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Type == "response.output_item.done" {
					items = append(items, envelope.Item)
				}
			}
			child.ReleaseDelivery()
		}
	} else {
		wire, err := child.TransformJSON(mustTestJSON(t, response))
		if err != nil {
			t.Fatal(err)
		}
		child.Delivered(wire)
		var output struct {
			Output []outputItem `json:"output"`
		}
		if err := json.Unmarshal(wire, &output); err != nil {
			t.Fatal(err)
		}
		items = output.Output
	}
	child.ReleaseDelivery()
	f := nativeChildCompletionFixture{proxy: p, workspace: workspace, source: appServerItem{ID: "provider-answer", Type: "agentMessage", Phase: "final_answer", Text: nativeChildOriginal}}
	var originalCount int
	for _, item := range items {
		var text strings.Builder
		for _, part := range item.Content {
			text.WriteString(part.Text)
		}
		if item.ID == f.source.ID {
			originalCount++
			if item.Phase != "final_answer" || text.String() != nativeChildOriginal {
				t.Fatalf("provider source changed: %+v", item)
			}
		} else if item.Phase == "final_answer" {
			if f.enriched.ID != "" {
				t.Fatal("duplicate enriched host completion")
			}
			f.enriched = appServerItem{ID: item.ID, Type: "agentMessage", Phase: item.Phase, Text: text.String()}
		}
	}
	// JSON has always replaced the source in its single terminal response;
	// SSE must leave the already-delivered provider item unchanged.
	if (stream && originalCount != 1) || (!stream && originalCount != 0) || f.enriched.ID == "" {
		t.Fatalf("host transcript lost original or enriched result: %+v", items)
	}
	for _, want := range []string{nativeChildOriginal, "Focused validation passed.", "**Changes:**"} {
		if !strings.Contains(f.enriched.Text, want) {
			t.Fatalf("enriched completion lost %q: %s", want, f.enriched.Text)
		}
	}
	record, ok, err := p.replayStore.read(workspace, f.enriched.ID, true)
	if err != nil || !ok || record.Replacement == nil {
		t.Fatalf("replacement not durable: %+v, %v, %v", record, ok, err)
	}
	if record.Replacement.Thread != "child" || record.Replacement.Turn != "child-turn" || !reflect.DeepEqual(record.Replacement.Items, []string{f.source.ID}) {
		t.Fatalf("wrong replacement scope: %+v", record.Replacement)
	}
	return f
}

func (f nativeChildCompletionFixture) ui(t *testing.T, proxy *mekugiProxy) *appServerUI {
	t.Helper()
	u := newAppServerSessionTestUI(t, f.workspace)
	u.proxy = proxy
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	return u
}

func nativeChildCompletionNotify(t *testing.T, u *appServerUI, thread, turn string, item appServerItem) {
	t.Helper()
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": item})
}

func assertNativeChildCompletion(t *testing.T, u *appServerUI, f nativeChildCompletionFixture) {
	t.Helper()
	var mainSeq, activitySeq uint64
	for _, view := range []*liveActivityView{u.view, u.agents} {
		count := 0
		for _, entry := range view.entries {
			if entry.native == nil || entry.native.thread != "child" || entry.native.turn != "child-turn" {
				continue
			}
			if entry.native.item == f.source.ID {
				t.Fatalf("raw completion remains visible: %+v", entry.activityPaneEntry)
			}
			if entry.native.item != f.enriched.ID {
				continue
			}
			count++
			if entry.Text != f.enriched.Text || entry.Kind != "final" {
				t.Fatalf("replacement lost full final answer: %+v", entry.activityPaneEntry)
			}
			if view == u.view {
				mainSeq, activitySeq = entry.Seq, entry.activitySeq
			}
		}
		if count != 1 {
			t.Fatalf("completion count %d, want one; entries=%+v", count, view.entries)
		}
	}
	if mainSeq == 0 || activitySeq == 0 || !u.shell.openActivityReply(mainSeq) {
		t.Fatal("enriched completion lost exact Activity reply link")
	}
	u.shell.output.layout(110)
	if !strings.Contains(u.shell.output.laid.Text, nativeChildOriginal) || !strings.Contains(u.shell.output.laid.Text, "Focused validation passed.") || !strings.Contains(u.shell.output.laid.Text, "**Changes:**") {
		t.Fatalf("dialog lost full enriched reply: %s", u.shell.output.laid.Text)
	}
}

func TestNativeChildCompletionNotificationOrder(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, enrichedFirst := range []bool{false, true} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+map[bool]string{false: "/source-first", true: "/enriched-first"}[enrichedFirst], func(t *testing.T) {
				f := newNativeChildCompletionFixture(t, stream)
				u := f.ui(t, f.proxy)
				items := []appServerItem{f.source, f.enriched}
				if enrichedFirst {
					items[0], items[1] = items[1], items[0]
				}
				for range 2 {
					for _, item := range items {
						nativeChildCompletionNotify(t, u, "child", "child-turn", item)
					}
					appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "child-turn", "status": "completed"}})
					assertNativeChildCompletion(t, u, f)
				}
				// Identical text is not identity. A different turn, child, or root
				// must retain its provider answer even when the source ID is reused.
				for _, scope := range [][2]string{{"child", "other-turn"}, {"other-child", "child-turn"}, {"main", "child-turn"}} {
					nativeChildCompletionNotify(t, u, scope[0], scope[1], f.source)
					view := u.agents
					if scope[0] == "main" {
						view = u.view
					}
					found := false
					for _, entry := range view.entries {
						if entry.native != nil && entry.native.thread == scope[0] && entry.native.turn == scope[1] && entry.Text == nativeChildOriginal {
							found = true
						}
					}
					if !found {
						t.Fatalf("unrelated provider answer hidden in %v", scope)
					}
				}
			})
		}
	}
}

func TestNativeChildCompletionRestoredDurableProvenance(t *testing.T) {
	f := newNativeChildCompletionFixture(t, true)
	p := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(f.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.snapshots.close)
	p.replayStore = store
	u := f.ui(t, p)
	// Use normal restore orchestration so Main receives the linked Activity copy.
	root := appServerThreadInfo{ID: "main", Cwd: f.workspace, Turns: []appServerHistoryTurn{{ID: "root-turn", Status: "completed", Items: []appServerItem{{ID: "spawn", Type: "collabAgentToolCall", Tool: "spawnAgent", Status: "completed", SenderThreadID: "main", ReceiverThreadIDs: []string{"child"}, Prompt: "Review the change."}}}}}
	if err := u.restorePaneContent(root); err != nil {
		t.Fatal(err)
	}
	restoreContentReply(t, u, 0, map[string]any{"data": []any{appServerThreadInfo{ID: "child", Cwd: f.workspace, ParentThreadID: "main", AgentNickname: "worker"}}, "nextCursor": nil})
	restoreContentReply(t, u, 1, map[string]any{"data": []any{}, "nextCursor": nil})
	restoreContentReply(t, u, 2, map[string]any{"thread": appServerThreadInfo{ID: "child", Cwd: f.workspace, ParentThreadID: "main", AgentNickname: "worker", Turns: []appServerHistoryTurn{{ID: "child-turn", Status: "completed", Items: []appServerItem{f.enriched}}}}})
	assertNativeChildCompletion(t, u, f)
	u.restoreActivityThread(appServerThreadInfo{ID: "child", Cwd: f.workspace, Turns: []appServerHistoryTurn{{ID: "child-turn", Status: "completed", olderPage: true, Items: []appServerItem{f.source}}}})
	nativeChildCompletionNotify(t, u, "child", "child-turn", f.source)
	assertNativeChildCompletion(t, u, f)
}

func TestNativeChildCompletionUntrustedProvenance(t *testing.T) {
	f := newNativeChildCompletionFixture(t, false)
	for _, kind := range []string{"prefix-only", "missing", "wrong-thread", "wrong-turn", "wrong-workspace", "different-source"} {
		t.Run(kind, func(t *testing.T) {
			p := newManagedMekugiProxy(t)
			attachTestReplayStore(t, p)
			u := f.ui(t, p)
			enriched := f.enriched
			replacement := &commentaryReplacement{Thread: "child", Turn: "child-turn", Items: []string{f.source.ID}}
			workspace := f.workspace
			switch kind {
			case "prefix-only":
				enriched.ID = "msg_mekugi_commentary_spoof"
			case "wrong-thread":
				replacement.Thread = "other"
			case "wrong-turn":
				replacement.Turn = "other"
			case "wrong-workspace":
				workspace = t.TempDir()
			case "different-source":
				replacement.Items = []string{"different"}
			}
			if kind != "prefix-only" && kind != "missing" {
				if err := p.replayStore.putCommentaryReplacing(t.Context(), workspace, []string{enriched.ID}, replacement); err != nil {
					t.Fatal(err)
				}
			}
			nativeChildCompletionNotify(t, u, "child", "child-turn", f.source)
			nativeChildCompletionNotify(t, u, "child", "child-turn", enriched)
			for _, view := range []*liveActivityView{u.view, u.agents} {
				found := false
				for _, entry := range view.entries {
					if entry.native != nil && entry.native.item == f.source.ID && entry.Text == nativeChildOriginal {
						found = true
					}
				}
				if !found {
					t.Fatal("untrusted provenance hid provider content")
				}
			}
		})
	}
}

func TestUISnapshotNativeChildCompletion(t *testing.T) {
	f := newNativeChildCompletionFixture(t, true)
	u := f.ui(t, f.proxy)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.view.painter.Theme = livediff.DarkTheme
	u.agents.painter.Theme = livediff.DarkTheme
	nativeChildCompletionNotify(t, u, "child", "child-turn", f.source)
	nativeChildCompletionNotify(t, u, "child", "child-turn", f.enriched)
	assertNativeChildCompletion(t, u, f)
	for _, view := range []*liveActivityView{u.view, u.agents} {
		for i := range view.entries {
			view.entries[i].Observed = now
		}
	}
	assertNativeUISnapshot(t, "native-child-completion-main", u.view.renderFeed(100, 80).lines)
	assertNativeUISnapshot(t, "native-child-completion-activity", u.agents.renderFeed(100, 80).lines)
	rows := make([]string, 30)
	u.shell.paintOutput(rows, 110, len(rows))
	assertNativeUISnapshot(t, "native-child-completion-dialog", rows)
}

func TestNativeChildCompletionAsForegroundThread(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, order := range []string{"source-first", "enriched-first", "resumed"} {
			t.Run(map[bool]string{false: "json", true: "sse"}[stream]+"/"+order, func(t *testing.T) {
				f := newNativeChildCompletionFixture(t, stream)
				u := newAppServerSessionTestUI(t, f.workspace)
				u.proxy, u.thread = f.proxy, "child"
				u.session.start("child", f.workspace)
				var sourceSeq uint64
				var sourceObserved time.Time
				if order == "resumed" {
					u.restoreMainHistory([]appServerHistoryTurn{{ID: "child-turn", Status: "completed", Items: []appServerItem{f.source, f.enriched}}}, nil, nil)
				} else {
					if order == "source-first" {
						nativeChildCompletionNotify(t, u, "child", "child-turn", f.source)
						for _, entry := range u.view.entries {
							if entry.native != nil && entry.native.item == f.source.ID {
								sourceSeq, sourceObserved = entry.Seq, entry.Observed
							}
						}
					}
					appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "child-turn", "item": f.enriched})
					nativeChildCompletionNotify(t, u, "child", "child-turn", f.enriched)
					// Repeated starts and completions must not revive either card.
					appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "child-turn", "item": f.enriched})
					nativeChildCompletionNotify(t, u, "child", "child-turn", f.enriched)
					nativeChildCompletionNotify(t, u, "child", "child-turn", f.source)
				}
				count := 0
				for _, entry := range u.view.entries {
					if entry.native == nil || entry.native.thread != "child" || entry.native.turn != "child-turn" {
						continue
					}
					if entry.native.item == f.source.ID {
						t.Fatal("foreground child retained the short duplicate")
					}
					if entry.native.item == f.enriched.ID {
						count++
						if entry.Text != f.enriched.Text {
							t.Fatal("foreground child lost enriched reply")
						}
						if sourceSeq != 0 && (entry.Seq != sourceSeq || !entry.Observed.Equal(sourceObserved)) {
							t.Fatal("foreground completion lost the source card's stable identity")
						}
					}
				}
				if count != 1 {
					t.Fatalf("foreground completion count=%d, want one", count)
				}
			})
		}
	}
}

func TestNativeChildCompletionReplacesMultipleSourceCards(t *testing.T) {
	for _, placeholder := range []string{"none", "before", "after"} {
		t.Run(placeholder, func(t *testing.T) {
			v := newLiveActivityView()
			entries := []activityPaneEntry{
				{Seq: 1, Kind: "final", Agent: "/root/worker", Text: "First part", native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "first", phase: "item/completed"}},
				{Seq: 2, Kind: "text", Agent: "Main", Text: "Unrelated message", native: &liveActivityNativeItem{thread: "main", turn: "turn", item: "main", phase: "item/completed"}},
				{Seq: 3, Kind: "final", Agent: "/root/worker", Text: "Second part", native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "second", phase: "item/completed"}},
				{Seq: 4, Kind: "final", Agent: "/root/worker", Text: "First part\n\nSecond part\n\nComplete evidence", native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "summary", phase: "item/completed", replacesItems: []string{"first", "second"}}},
			}
			if placeholder == "before" {
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Kind: "final", Agent: "/root/worker", native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "summary", phase: "item/started"}}}})
				for i := range entries {
					entries[i].Seq++
				}
			}
			v.apply(activityPaneEvent{Kind: "entries", Entries: entries[:3]})
			if placeholder == "after" {
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 4, Kind: "final", Agent: "/root/worker", native: &liveActivityNativeItem{thread: "child", turn: "turn", item: "summary", phase: "item/started"}}}})
				entries[3].Seq++
			}
			v.apply(activityPaneEvent{Kind: "entries", Entries: entries[3:]})
			if len(v.entries) != 2 || v.entries[0].Seq != entries[0].Seq || v.entries[0].Text != entries[3].Text || v.entries[1].Text != "Unrelated message" {
				t.Fatalf("multi-part reply changed unrelated entries or lost its stable link: %+v", v.entries)
			}
			if v.entrySeq(entries[0]) != entries[0].Seq || v.entrySeq(entries[2]) != entries[0].Seq || v.entrySeq(entries[3]) != entries[0].Seq {
				t.Fatal("source cards no longer resolve to the complete reply")
			}
		})
	}
}
