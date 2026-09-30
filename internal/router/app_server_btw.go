package router

import (
	json "encoding/json/v2"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// The dock owns presentation and RPC correlation only. Codex owns the snapshot,
// conversation history, execution, cancellation and unloading of this thread.
type appServerBTW struct {
	thread, turn, question, status string
	pending                        composerDraft
	answer                         []btwAnswer
	busy, starting, closed, alert  bool
	interrupting, unloading        bool
	truncated                      bool
	attachmentNotice               string
	scroll, rows                   int
}

type btwAnswer struct{ id, text string }
type btwRequest struct {
	panel  *appServerBTW
	method string
}

func (u *appServerUI) submitBTW() error {
	text := strings.TrimSpace(u.draft)
	question := strings.TrimSpace(strings.TrimPrefix(text, "/btw"))
	if question == "" {
		u.setNotice("Use /btw QUESTION · repeat /btw for a follow-up · Esc closes", false)
		return nil
	}
	if u.thread == "" || u.restoring != nil || u.clearing {
		u.setNotice("Wait for the conversation to load before asking a side question", false)
		return nil
	}
	if u.btw != nil && (u.btw.busy || u.btw.starting) {
		u.setNotice("Side answer still running · wait for it or Esc to close", false)
		return nil
	}
	if u.btw == nil {
		u.btw = new(appServerBTW)
	}
	b := u.btw
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := strings.Index(u.draft, "/btw") + len("/btw")
	u.deleteDraftRange(0, prefix)
	b.pending = u.takeDraft()
	b.attachmentNotice = b.pending.attachmentNotice
	b.question, b.answer, b.scroll = question, nil, 0
	b.truncated = false
	b.busy, b.alert, b.status = true, false, "Waiting for Main's submission…"
	u.setNotice("", false)
	return u.flushBTW()
}

func (u *appServerUI) btwRequest(b *appServerBTW, method string, params any) error {
	id, err := u.client.Send(method, params, true)
	if err != nil {
		return err
	}
	if u.btwRequests == nil {
		u.btwRequests = make(map[string]btwRequest)
	}
	u.btwRequests[id] = btwRequest{b, method}
	return nil
}

func (u *appServerUI) flushBTW() error {
	b := u.btw
	if b == nil || b.pending.text == "" || b.starting || u.clearing {
		return nil
	}
	parts := []composerDraft{b.pending}
	if u.waitForSkillBindings(parts) {
		return nil
	}
	u.bindSkills(&parts[0], true)
	u.snapshotDraftSkills(&parts[0])
	b.pending = parts[0]
	if b.thread == "" {
		// A just-submitted main message must reach Codex before its snapshot.
		// An already-running turn is deliberately not interrupted or awaited.
		if u.starting || u.submission.text != "" || u.settingsPending {
			return nil
		}
		b.starting, b.status = true, "Branching…"
		params := map[string]any{
			"threadId": u.thread, "ephemeral": true, "excludeTurns": true,
			"sandbox": "read-only", "approvalPolicy": "never",
		}
		// Fork defaults come from the app-server invocation, not necessarily
		// Main's live settings after a model switch or resume.
		if u.model != "" {
			params["model"] = u.model
		}
		config := make(map[string]any)
		if u.reasoningEffort != "" {
			config["model_reasoning_effort"] = u.reasoningEffort
		}
		if len(config) > 0 {
			params["config"] = config
		}
		params["serviceTier"] = nil
		if u.serviceTier != "" {
			params["serviceTier"] = u.serviceTier
		}
		return u.btwRequest(b, "thread/fork", params)
	}
	b.starting, b.status = true, "Answering…"
	input := b.pending.input()
	input = append(appserver.Input("This is a /btw side question. Answer only the side question using the conversation context. Do not continue the main task or use tools."), input...)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(b.pending.attachments) > 0 || len(b.pending.skills) > 0) && textBytes > composerTextLimit {
		u.failBTW(b, "Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.")
		return nil
	}
	for _, image := range b.pending.images {
		delete(u.ownedImages, image.path)
	}
	return u.btwRequest(b, "turn/start", map[string]any{
		"threadId": b.thread, "input": input, "environments": []any{},
	})
}

func (u *appServerUI) failBTW(b *appServerBTW, message string) {
	b.busy, b.starting, b.alert, b.status = false, false, true, message
	if !b.closed && b.pending.text != "" {
		// Reuse the composer restorer to retain image/file tokens and any newer
		// draft. The command and question may be separated by a newline.
		u.restoreDrafts(joinDrafts(composerDraft{text: "/btw"}, b.pending))
		u.setNotice("Side question restored ahead of draft · edit before resending", true)
	}
	b.pending = composerDraft{}
}

func (u *appServerUI) closeBTW() error {
	b := u.btw
	if b == nil {
		return nil
	}
	u.btw, b.closed = nil, true
	b.pending, b.answer = composerDraft{}, nil
	u.pruneDraftImages()
	return u.releaseBTW(b)
}

func (u *appServerUI) releaseBTW(b *appServerBTW) error {
	if b.thread == "" || b.starting || b.interrupting || b.unloading {
		return nil // The correlated response will finish cancellation.
	}
	if b.turn != "" {
		b.interrupting = true
		return u.btwRequest(b, "turn/interrupt", map[string]any{"threadId": b.thread, "turnId": b.turn})
	}
	b.unloading = true
	return u.btwRequest(b, "thread/unsubscribe", map[string]any{"threadId": b.thread})
}

func (u *appServerUI) btwMessage(m appserver.Message) (bool, error) {
	if m.Method == "" {
		r, ok := u.btwRequests[string(m.ID)]
		if !ok {
			return false, nil
		}
		delete(u.btwRequests, string(m.ID))
		b := r.panel
		u.dirty = true
		if r.method == "turn/interrupt" {
			b.turn, b.interrupting = "", false
			if m.Error != nil {
				u.setNotice("Could not interrupt side answer: "+m.Error.Message, true)
			}
			return true, u.releaseBTW(b)
		}
		if r.method == "thread/unsubscribe" {
			if m.Error != nil {
				u.setNotice("Could not unload side conversation: "+m.Error.Message, true)
			}
			return true, nil
		}
		b.starting = false
		if m.Error != nil {
			u.failBTW(b, m.Error.Message)
			if b.closed {
				return true, u.releaseBTW(b)
			}
			return true, nil
		}
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			u.failBTW(b, "Invalid side-thread response: "+err.Error())
			return true, nil
		}
		switch r.method {
		case "thread/fork":
			if result.Thread.ID == "" || result.Thread.ID == u.thread {
				u.failBTW(b, "Codex did not return a separate side thread")
				return true, nil
			}
			b.thread = result.Thread.ID
			if u.btwThreads == nil {
				u.btwThreads = make(map[string]*appServerBTW)
			}
			u.btwThreads[b.thread] = b
		case "turn/start":
			if result.Turn.ID == "" {
				u.failBTW(b, "Codex returned no side-turn identity")
			}
			if b.busy {
				b.turn = result.Turn.ID
			}
			b.pending = composerDraft{}
		}
		if b.closed {
			return true, u.releaseBTW(b)
		}
		return true, u.flushBTW()
	}
	if len(u.btwThreads) == 0 {
		return false, nil
	}
	var p struct {
		appServerEvent
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return false, nil // The normal dispatcher owns other message shapes.
	}
	thread := p.ThreadID
	if m.Method == "thread/started" {
		thread = p.Thread.ID
	}
	b := u.btwThreads[thread]
	if b == nil {
		return false, nil
	}
	u.dirty = true
	if m.Method == "thread/closed" {
		delete(u.btwThreads, thread)
		if !b.closed {
			b.thread, b.turn, b.busy, b.starting = "", "", false, false
			b.status, b.alert = "Side conversation closed · /btw starts a new branch", true
		}
		return true, nil
	}
	if len(m.ID) != 0 {
		b.status, b.alert = "Side answer blocked on "+m.Method+" · Esc closes", true
		return true, nil
	}
	switch m.Method {
	case "turn/started":
		b.turn = p.Turn.ID
	case "turn/completed":
		if b.turn != "" && b.turn != p.Turn.ID {
			return true, nil
		}
		b.turn, b.busy = "", false
		b.status, b.alert = p.Turn.Status, p.Turn.Status == "failed"
		if p.Turn.Error != nil {
			b.status = p.Turn.Error.Message
		}
	case "error":
		if p.Error != nil {
			b.status, b.alert = p.Error.Message, true
		}
	case "item/agentMessage/delta", "item/completed":
		if b.closed || p.TurnID != b.turn {
			return true, nil
		}
		if m.Method == "item/completed" && p.Item.Type != "agentMessage" {
			return true, nil
		}
		id := p.ItemID
		if id == "" {
			id = p.Item.ID
		}
		index := -1
		for i := range b.answer {
			if b.answer[i].id == id {
				index = i
				break
			}
		}
		if index < 0 {
			if len(b.answer) >= 256 {
				b.truncated = true
				return true, nil
			}
			b.answer = append(b.answer, btwAnswer{id: id})
			index = len(b.answer) - 1
		}
		if m.Method == "item/completed" {
			b.answer[index].text = ""
			b.appendAnswer(index, p.Item.Text)
		} else {
			b.appendAnswer(index, p.Delta)
		}
	}
	return true, nil
}

// The model's full context stays in Codex; the transient display is bounded.
func (b *appServerBTW) appendAnswer(index int, text string) {
	room := 256 << 10
	for _, item := range b.answer {
		room -= len(item.text)
	}
	if len(text) > room {
		for room > 0 && !utf8.RuneStart(text[room]) {
			room--
		}
		text, b.truncated = text[:room], true
	}
	b.answer[index].text += text
}

func (u *appServerUI) btwKey(key string) (bool, error) {
	b := u.btw
	if b == nil {
		return false, nil
	}
	switch key {
	case "\x1b":
		return true, u.closeBTW()
	case "\x1b[5~":
		b.scroll += max(1, b.rows-1)
		return true, nil
	case "\x1b[6~":
		b.scroll = max(0, b.scroll-max(1, b.rows-1))
		return true, nil
	}
	return false, nil
}

func (u *appServerUI) btwRows(width, height int) []string {
	b := u.btw
	if b == nil || height < 1 {
		return nil
	}
	color := u.view.painter.Theme.Accent()
	if b.alert {
		color = activityui.Red
	}
	label := " /btw · " + livediff.Safe(b.status, false)
	rows := []string{ansi.Truncate(color+"╭─"+label+activityui.Reset, width, "…")}
	inner := max(1, width-2)
	question := activityui.Wrap(livediff.Safe(b.question, false), inner, true)
	for _, row := range question[:min(2, len(question))] {
		rows = append(rows, ansi.Truncate(color+"│ "+activityui.Reset+row, width, "…"))
	}
	if b.attachmentNotice != "" {
		rows = append(rows, ansi.Truncate(activityui.Red+"│ "+livediff.Safe(b.attachmentNotice, false)+activityui.Reset, width, "…"))
	}
	var answer []string
	for _, item := range b.answer {
		answer = append(answer, u.view.painter.Markdown(livediff.Safe(item.text, false), inner)...)
	}
	b.rows = max(0, height-len(rows)-1)
	b.scroll = min(b.scroll, max(0, len(answer)-b.rows))
	end := len(answer) - b.scroll
	for _, row := range answer[max(0, end-b.rows):end] {
		rows = append(rows, ansi.Truncate(color+"│ "+activityui.Reset+row, width, ""))
	}
	hint := "╰─ /btw follow-up · Esc close"
	if len(answer) > b.rows {
		hint += " · PgUp/PgDn scroll"
	}
	if b.truncated {
		hint = "╰─ Display truncated · Esc close"
	}
	rows = append(rows, ansi.Truncate(activityui.Dim+hint+activityui.Reset, width, "…"))
	return rows[:min(len(rows), height)]
}
