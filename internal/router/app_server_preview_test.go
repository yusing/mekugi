package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"golang.org/x/term"
)

// TestNativeUIPreview uses the actual terminal shell and Activity painters.
// It is local and makes no model, network, or workspace requests.
func TestNativeUIPreview(t *testing.T) {
	if os.Getenv("MEKUGI_NATIVE_UI_PREVIEW") != "1" {
		t.Skip("interactive native UI preview")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	defer tty.Close()
	p := newNativePreview(t)
	defer p.ui.shell.diff.close()
	defer p.ui.shell.diffScreen.Close()
	err = withRawPane(t.Context(), tty, tty, "\x1b[?1049h\x1b[?25l\x1b[?1003;1006;2004h", "\x1b[?2026l\x1b[?1003;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
		tick := time.NewTicker(33 * time.Millisecond)
		defer tick.Stop()
		lastWidth, lastHeight := 0, 0
		lastPaint := time.Time{}
		lastStep := time.Now()
		paint := func() error {
			w, h, err := term.GetSize(int(tty.Fd()))
			if err != nil {
				return err
			}
			if err := p.ui.paint(tty, w, h); err != nil {
				return err
			}
			lastWidth, lastHeight, lastPaint = w, h, time.Now()
			return nil
		}
		if err := paint(); err != nil {
			return err
		}
		for {
			select {
			case <-t.Context().Done():
				return t.Context().Err()
			case <-tick.C:
				flashExpired := p.ui.view.expireFlash(time.Now())
				if time.Since(lastStep) >= 650*time.Millisecond && p.advance() {
					lastStep = time.Now()
					if err := paint(); err != nil {
						return err
					}
					continue
				}
				w, h, err := term.GetSize(int(tty.Fd()))
				if err != nil {
					return err
				}
				if w != lastWidth || h != lastHeight || time.Since(lastPaint) >= time.Second || p.ui.agents.hasLiveReasoning() || flashExpired {
					if err := paint(); err != nil {
						return err
					}
				}
			case key, ok := <-keys:
				if !ok {
					return io.EOF
				}
				if key == 3 || key == 4 {
					return nil
				}
				if p.step < len(p.steps) && p.ui.shell.focus == 0 && key == '\r' && !p.ui.paste {
					continue // Keep typed input as a draft until playback completes.
				}
				if err := p.ui.shell.key(key); err != nil {
					return err
				}
				if p.ui.submitted != "" {
					p.completeMessage()
				}
				if err := paint(); err != nil {
					return err
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeUIPreviewRenderedFrame(t *testing.T) {
	p := newNativePreview(t)
	defer p.ui.shell.diff.close()
	defer p.ui.shell.diffScreen.Close()
	p.ui.draft = "Keep my draft"
	for p.advance() {
	}
	if p.ui.draft != "Keep my draft" {
		t.Fatal("playback overwrote typed input")
	}
	screen := vt.NewEmulator(160, 48)
	defer screen.Close()
	if err := p.ui.paint(screen, 160, 48); err != nil {
		t.Fatal(err)
	}
	frame := screen.String()
	for _, content := range []string{"↩ reply to your message", "↩ reply to assignment", "✓ reviewer finished", "◆ journal", "✓ answer", "The edited answer still links", "AGENTS", "1 Main"} {
		if !strings.Contains(frame, content) {
			t.Fatalf("preview does not show %q:\n%s", content, frame)
		}
	}
	if strings.Contains(frame, "Temporary milestone to delete") || strings.Contains(frame, "/journal") {
		t.Fatal("preview retained a deleted milestone or command menu")
	}
	t.Logf("Native preview:\n%s", frame)
}

type nativePreview struct {
	t        *testing.T
	ui       *appServerUI
	input    *appServerTestInput
	message  int
	sequence uint64
	steps    []func()
	step     int
}

func newNativePreview(t *testing.T) *nativePreview {
	u, input := newAppServerTestUI()
	u.ctx = t.Context()
	u.ensureShell()
	p := &nativePreview{t: t, ui: u, input: input}
	p.populate()
	return p
}

func (p *nativePreview) advance() bool {
	if p.step >= len(p.steps) {
		return false
	}
	p.steps[p.step]()
	p.step++
	return true
}

func (p *nativePreview) populate() {
	u := p.ui
	now := time.Now()
	agents := []activityPaneAgent{{Name: "/root", Role: "main", Started: now}}
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: agents})
	u.agents.status = ""
	u.status = "Preview"
	task := "Please review the journal answer link and report any presentation issue.\n\n" +
		"Context: after a journal edit, the answer shown in Main should still point back to the question it answers, " +
		"even when I steered the turn with another prompt in between. Check both the live milestone and the flushed answer, " +
		"and tell me if anything reads as duplicated, clipped, or attributed to the wrong author."
	assignment := "Does the journal answer visibly link to the original question? Inspect `journal_native_ui.go` and " +
		"`live_activity_conversation.go`, then report whether the link survives a journal edit, a retraction, and a " +
		"terminal flush. Keep the report short; I only need the verdict and any concrete failure."
	followup := "Check the answer link again after the journal edit."
	collector := newSubagentActivity()
	generation, _ := collector.attachNativePane("main")
	initialTask := journalTestAssignment("/root/reviewer", "NEW_TASK", assignment)
	initialTask["id"] = "task-1"
	followupTask := journalTestAssignment("/root/reviewer", "NEW_TASK", followup)
	followupTask["id"] = "task-2"
	assign := func(items ...any) {
		request, err := parseResponsesRequest(mustTestJSON(p.t, map[string]any{"model": "gpt-6-astra", "input": items}))
		if err != nil {
			p.t.Fatal(err)
		}
		collector.collectSubagentStart("child", &request, "/root/reviewer")
		entries, _, _ := collector.takePane(generation)
		for i := range entries {
			p.sequence++
			entries[i].Seq = p.sequence
		}
		u.applyActivity(entries, agents)
	}
	activity := func(agent, kind, body, callID string) {
		p.sequence++
		u.applyActivity([]activityPaneEntry{{Seq: p.sequence, Agent: agent, Kind: kind, Text: body, CallID: callID, Observed: time.Now()}}, agents)
	}
	p.steps = []func(){
		func() {
			agents[0].Responding = true
			u.applyActivity(nil, agents)
			p.beginMessage(task)
		},
		func() {
			p.notify("item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "preview-turn-1", "itemId": "main-note", "delta": "I will inspect the journal link, then hand the edit-and-flush check to a reviewer so both paths are covered:\n\n"})
		},
		func() {
			p.notify("item/agentMessage/delta", map[string]any{"threadId": "main", "turnId": "preview-turn-1", "itemId": "main-note", "delta": "- the live milestone while the turn runs\n- the flushed answer and its `↩` link after an edit\n- a retracted milestone, which should disappear rather than leave a placeholder"})
		},
		func() {
			p.notify("item/completed", map[string]any{"threadId": "main", "turnId": "preview-turn-1", "item": map[string]any{"id": "main-note", "type": "agentMessage", "text": "I will inspect the journal link, then hand the edit-and-flush check to a reviewer so both paths are covered:\n\n- the live milestone while the turn runs\n- the flushed answer and its `↩` link after an edit\n- a retracted milestone, which should disappear rather than leave a placeholder"}})
		},
		func() {
			agents = append(agents, activityPaneAgent{Name: "/root/reviewer", Role: "review", Started: time.Now(), Responding: true})
			collector.observe("child", "main", "/root/reviewer", true)
			assign(initialTask)
			activity("/root", "reply", "[`/root` -> `/root/reviewer`] Message sent:\nPlease also check the target after a journal edit.", "")
		},
		func() {
			activity("/root/reviewer", "text", "I am checking the answer against the original question.", "")
		},
		func() {
			activity("/root/reviewer", "tool", "Read `journal_native_ui.go 1:160`", "read-1")
		},
		func() {
			activity("/root/reviewer", "tool", "Read `live_activity_conversation.go 200:330`", "read-2")
		},
		func() {
			activity("/root/reviewer", "tool", "Search `questionLink|questionTarget` in `internal/router`", "search-1")
		},
		func() {
			activity("/root/reviewer", "tool", "Run\n```bash\ngo test ./internal/router -run 'TestLiveActivity(QuestionLinks|MainJournal)' -count=1\n```", "run-1")
		},
		func() {
			u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "amber", Text: "Reviewer is checking the question link.", Author: "/root", Created: 1}})
		},
		func() {
			activity("/root/reviewer", "reasoning", "**Checking the question target**", "reasoning-1")
		},
		func() {
			activity("/root/reviewer", "reasoning", "**Checking the question target**\n\nComparing the original target with the edited journal revision.", "reasoning-1")
		},
		func() {
			u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "amber", Text: "Reviewer checked the link after a journal edit.", Author: "/root", Created: 1, Updated: 2}})
		},
		func() {
			u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "apple", Text: "Temporary milestone to delete.", Author: "/root", Created: 3}})
		},
		func() {
			u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "apple", Text: "Temporary milestone to delete.", Author: "/root", Created: 3}, retracted: true})
		},
		func() {
			activity("/root/reviewer", "reply", "[`/root/reviewer` -> `/root`] Message received:\nThe answer link returns to the original question. "+
				"I followed it from the flushed answer and from an edited revision; both land on the prompt that asked it, "+
				"not on the later steering prompt with similar wording. The retracted milestone no longer appears anywhere.", "")
		},
		func() {
			var child strings.Builder
			agents[1].Responding, agents[1].Final, agents[1].LastResponse = false, true, time.Now()
			child.WriteString("Journal result `/root/reviewer`")
			writeJournalItems(&child, []journalItem{{ID: "child1", Question: assignment, Text: "Yes. The answer links to the original question.\n\n" +
				"- **Edit:** the revised answer keeps its original target.\n" +
				"- **Retraction:** the milestone is removed; nothing is left in its place.\n" +
				"- **Flush:** the terminal batch replaces the live milestone once, without a duplicate.", Author: "/root/reviewer"}})
			activity("/root/reviewer", "final", child.String(), "")
		},
		func() {
			agents[1].Responding, agents[1].Final = true, false
			assign(initialTask, followupTask)
		},
		func() {
			activity("/root/reviewer", "reasoning", "**Checking the edited answer**", "reasoning-2")
		},
		func() {
			agents[1].Responding, agents[1].Final, agents[1].LastResponse = false, true, time.Now()
			var child strings.Builder
			child.WriteString("Journal result `/root/reviewer`")
			writeJournalItems(&child, []journalItem{{ID: "child2", Question: followup, Text: "The edited answer still links to the original question.", Author: "/root/reviewer"}})
			activity("/root/reviewer", "final", child.String(), "")
		},
		func() {
			u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "amber", Text: "Reviewer checked the link after a journal edit.", Author: "/root", Created: 1, Updated: 2}, terminal: true, batch: 1})
		},
		func() {
			u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "arch", Question: task, Text: "The journal answer links back to your original question, and the reviewer confirmed it remains stable after editing.\n\n" +
				"What I checked:\n\n" +
				"1. A live milestone appears under **journal** while the turn runs.\n" +
				"2. An edit replaces that milestone in place instead of adding a second copy.\n" +
				"3. A retraction removes the milestone entirely.\n" +
				"4. The flushed answer shows a `↩ reply to your message` link that jumps to this prompt.\n\n" +
				"The link target is resolved from the retained entry sequence, not from matching text:\n\n" +
				"```go\nif question, ok := v.questionLink(group, index); ok {\n\ttarget = question.Seq\n}\n```\n\n" +
				"No presentation issue remains in this flow.", Author: "/root", Created: 4}, terminal: true, batch: 1})
		},
		func() {
			agents[0].Responding, agents[0].LastResponse = false, time.Now()
			u.applyActivity(nil, agents)
			p.notify("turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "preview-turn-1", "status": "completed"}})
			u.status = "Completed"
		},
	}
}

func (p *nativePreview) notify(method string, params any) {
	wire, err := json.Marshal(params)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.ui.message(appServerMessage{Method: method, Params: jsontext.Value(wire)}); err != nil {
		p.t.Fatal(err)
	}
}

func (p *nativePreview) completeMessage() {
	body := p.ui.submitted
	turn := p.beginMessage(body)
	p.notify("item/completed", map[string]any{"threadId": "main", "turnId": turn, "item": map[string]any{
		"id": fmt.Sprintf("preview-answer-%d", p.message), "type": "agentMessage", "text": "I heard: " + body,
	}})
	p.notify("turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn, "status": "completed"}})
	p.ui.status = "Completed"
}

func (p *nativePreview) beginMessage(body string) string {
	p.message++
	turn := fmt.Sprintf("preview-turn-%d", p.message)
	p.notify("turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": turn}})
	p.notify("item/completed", map[string]any{"threadId": "main", "turnId": turn, "item": map[string]any{
		"id": fmt.Sprintf("preview-user-%d", p.message), "type": "userMessage",
		"content": []map[string]string{{"type": "text", "text": body}},
	}})
	id := strconv.Itoa(p.ui.client.next)
	result, err := json.Marshal(map[string]any{"turn": map[string]any{"id": turn}})
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.ui.message(appServerMessage{ID: jsontext.Value(id), Result: jsontext.Value(result)}); err != nil {
		p.t.Fatal(err)
	}
	p.input.Reset()
	return turn
}
