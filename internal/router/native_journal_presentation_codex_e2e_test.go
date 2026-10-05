//go:build journal_e2e

package router

import (
	json "encoding/json/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
)

// Exercise manual standalone compaction without driver-dispatched reset or a
// gated provider. Completion binds the retained answer to the unique host item.
func TestNativeJournalPresentationCodexE2E(t *testing.T) {
	ctx, cmd, provider := journalResetCodexFixture(t)
	proxy, workspace := provider.proxy, provider.workspace
	proxy.journalCompaction = "auto"
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		client.Close()
		if !exited {
			<-client.Done
		}
	}()
	u := newAppServerSessionTestUI(t, workspace)
	u.ctx, u.client, u.proxy = ctx, client, proxy
	initializeID, err := client.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	var startID string
	var firstDone, compactDone, itemDone bool
	var observed journalCompactionItem
	for !compactDone || u.compaction.ackPending {
		select {
		case m, ok := <-client.Messages:
			if !ok {
				t.Fatal("app-server closed before native compaction completion")
			}
			if m.Error != nil {
				t.Fatalf("app-server RPC: %s", m.Error.Message)
			}
			if m.Method == "" && string(m.ID) == initializeID {
				if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
					t.Fatal(err)
				}
				startID, err = client.Send("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"}, true)
				if err != nil {
					t.Fatal(err)
				}
				continue
			}
			if m.Method == "" && string(m.ID) == startID {
				var result struct {
					Thread appServerThreadInfo `json:"thread"`
				}
				if err := json.Unmarshal(m.Result, &result); err != nil {
					t.Fatal(err)
				}
				if result.Thread.ID == "" || result.Thread.Cwd != workspace {
					t.Fatalf("unexpected native thread: %+v", result.Thread)
				}
				u.thread = result.Thread.ID
				u.session.start(u.thread, workspace)
				u.journal = proxy.journals.attachNative(workspace, u.thread)
				u.unscopedJournal = proxy.journals.attachNative("", u.thread)
				t.Cleanup(func() {
					proxy.journals.detachNative(u.journal)
					proxy.journals.detachNative(u.unscopedJournal)
				})
				appServerTestKeys(t, u, "Complete the first journal slice.\r")
				continue
			}
			var event appServerEvent
			if m.Method != "" {
				if err := json.Unmarshal(m.Params, &event); err != nil {
					t.Fatal(err)
				}
			}
			if err := u.message(m); err != nil {
				t.Fatalf("UI event %s: %v", m.Method, err)
			}
			if u.reset != nil && u.reset.compactTurn != "" {
				t.Fatal("slice reset driver dispatched a compaction instead of manual /compact")
			}
			if event.ThreadID != u.thread {
				continue
			}
			if event.Item.Type == "contextCompaction" {
				switch m.Method {
				case "item/started":
					if !firstDone || event.TurnID == "" || event.Item.ID == "" || observed.Item != "" {
						t.Fatalf("unexpected native compaction start: %+v", event)
					}
					observed = journalCompactionItem{Turn: event.TurnID, Item: event.Item.ID}
				case "item/completed":
					if observed != (journalCompactionItem{Turn: event.TurnID, Item: event.Item.ID}) {
						t.Fatalf("native completion lacks matching start: observed=%+v event=%+v", observed, event)
					}
					text, _, handled := u.progress(event.Item, m.Method, event.ThreadID, event.TurnID)
					if !handled || text != "Context reset from journal" {
						t.Fatalf("native completion presentation = %q, handled=%t; host item=%+v", text, handled, observed)
					}
					frame := ansi.Strip(strings.Join(u.view.renderFeed(100, 30).lines, "\n"))
					if !strings.Contains(frame, "Context reset from journal") || strings.Contains(frame, "Context compacted") {
						t.Fatalf("native completion not rendered as journal reset:\n%s", frame)
					}
					itemDone = true
				}
			}
			if m.Method == "turn/completed" {
				if event.Turn.Status != "completed" {
					t.Fatalf("native turn failed: %+v", event.Turn)
				}
				if !firstDone {
					firstDone = true
					appServerTestKeys(t, u, "/compact\r")
				} else {
					if !itemDone || event.Turn.ID != observed.Turn {
						t.Fatalf("compaction turn ended without matching rendered item: %+v", event)
					}
					compactDone = true
				}
			}
		case err := <-client.Done:
			exited = true
			t.Fatalf("app-server exited before native compaction: %v", err)
		case <-ctx.Done():
			t.Fatalf("native compaction timed out: %v; observed=%+v status=%s", ctx.Err(), observed, u.status)
		}
	}
	provider.mu.Lock()
	turns, compactions := provider.turns, provider.compactions
	provider.mu.Unlock()
	if turns != 1 || compactions != 0 {
		t.Fatalf("manual journal reset reached provider: turns=%d compactions=%d", turns, compactions)
	}
	intent, err := proxy.replayStore.resetIntent(ctx, workspace, u.thread)
	if err != nil || intent != nil && (intent.Phase == "armed" || intent.Phase == "consumed") {
		t.Fatalf("ordinary manual compaction used slice-reset intent: %+v %v", intent, err)
	}
	fresh, err := openMekugiReplayStoreContext(ctx, proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	data, err := readManagedOutputFile(filepath.Join(fresh.directory, journalCompactionName(workspace, u.thread)))
	if err != nil {
		t.Fatal(err)
	}
	var record journalCompactionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Version != 1 || record.Workspace != workspace || record.Thread != u.thread || record.ResponseID == "" || len(record.AnsweredItems) != 1 {
		t.Fatalf("fresh-store receipt lost exact native provenance: %+v; host item=%+v", record, observed)
	}
	retained := record.AnsweredItems[0]
	if retained.Turn != observed.Turn || retained.Item != observed.Item || retained.ResponseID != record.ResponseID {
		t.Fatalf("fresh-store receipt lost exact native provenance: %+v; host item=%+v", record, observed)
	}
	if !fresh.answeredCompactionItem(ctx, workspace, u.thread, observed.Turn, observed.Item) ||
		fresh.answeredCompactionItem(ctx, workspace, u.thread, observed.Turn, "other-item") ||
		fresh.answeredCompactionItem(ctx, workspace, u.thread, "other-turn", observed.Item) {
		t.Fatal("fresh-store receipt did not preserve exact turn/item matching")
	}
	t.Logf("Native manual compaction retained host turn=%s item=%s response=%s; one local inference, zero provider compactions", observed.Turn, observed.Item, record.ResponseID)
}
