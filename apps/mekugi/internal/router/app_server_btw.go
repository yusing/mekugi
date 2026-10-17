package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// The adapter retains terminal interaction state and the host correlation owner.
// Its conversation state is the same entry exposed by the scoped module service.
type appServerBTW struct {
	*conversationmodel.SideAnswer
	sessioncontrol.Side
	scroll, rows int
	rect         terminalsession.Rect
	copyAnswer   []string
}

func (p *mekugiProxy) setBTWThread(thread string, active bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !active {
		delete(p.btwThreads, thread)
		return
	}
	if p.btwThreads == nil {
		p.btwThreads = make(map[string]bool)
	}
	p.btwThreads[thread] = true
}

func (p *mekugiProxy) isBTWThread(thread string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.btwThreads[thread]
}

func (u *appServerUI) submitBTW() error {
	text := strings.TrimSpace(u.Text)
	question := strings.TrimSpace(strings.TrimPrefix(text, "/btw"))
	if question == "" {
		u.setNotice("Use /btw QUESTION · repeat /btw for a follow-up · Esc closes", false)
		return nil
	}
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the conversation to load before asking a side question", false)
		return nil
	}
	if u.btw != nil && (u.btw.Busy || u.btw.Starting) {
		u.setNotice("Side answer still running · wait for it or Esc to close", false)
		return nil
	}
	if u.btw != nil && u.btw.Status == conversationmodel.SideCompactionRequired {
		u.setNotice(conversationmodel.SideCompactionRequired, true)
		return nil
	}
	if u.btw == nil {
		u.conversation().Side = new(conversationmodel.SideAnswer)
		u.btw = &appServerBTW{SideAnswer: u.conversation().Side}
	}
	b := u.btw
	// Remove the command through the composer so attachment offsets stay valid.
	prefix := strings.Index(u.Text, "/btw") + len("/btw")
	u.deleteDraftRange(0, prefix)
	b.Begin(question, u.takeDraft())
	b.scroll = 0
	b.Busy = true
	u.setNotice("", false)
	return u.flushBTW()
}

func (u *appServerUI) btwRequest(b *appServerBTW, method string, params any) error {
	_, err := u.requests.Send(method, params, "", appServerRequest{side: b})
	return err
}

func (u *appServerUI) flushBTW() error {
	b := u.btw
	if b == nil || b.Pending.Text == "" || b.Starting || b.Turn != "" || u.Replacement.Pending() {
		return nil
	}
	parts := []conversationmodel.Input{b.Pending}
	if u.waitForSkillBindings(parts) {
		return nil
	}
	u.bindSkills(&parts[0], true)
	u.snapshotDraftSkills(&parts[0])
	b.Pending = parts[0]
	if b.Thread == "" {
		// A just-submitted main message must reach Codex before its snapshot.
		// An already-running turn is deliberately not interrupted or awaited.
		if u.Starting() || u.InputPending() || u.Settings.Pending() {
			return nil
		}
		b.Starting, b.Status = true, "Branching…"
		params := map[string]any{
			"threadId": u.Thread, "ephemeral": true, "excludeTurns": true,
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
	b.Starting, b.Status = true, "Answering"
	b.Started = u.now()
	input := conversationbinding.Input(b.Pending.Draft)
	input = append(appserver.Input("This is a /btw side question. Answer only the side question using the conversation context. Do not continue the main task or use tools."), input...)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(b.Pending.Attachments) > 0 || len(b.Pending.Skills) > 0) && textBytes > composerTextLimit {
		u.failBTW(b, "Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.")
		return nil
	}
	for _, image := range b.Pending.Images {
		delete(u.ownedImages, image.Path)
	}
	return u.btwRequest(b, "turn/start", map[string]any{
		"threadId": b.Thread, "input": input, "environments": []any{},
	})
}

func (u *appServerUI) failBTW(b *appServerBTW, message string) {
	b.Busy, b.Starting = false, false
	if restored := b.Fail(message, b.Closed); restored.Text != "" {
		u.restoreDrafts(restored)
		u.setNotice("Side question restored ahead of draft · edit before resending", true)
	}
}

func (u *appServerUI) closeBTW() error {
	b := u.btw
	if b == nil {
		return nil
	}
	u.btw, b.Closed = nil, true
	b.Close()
	u.conversation().Side = nil
	u.pruneDraftImages()
	return u.releaseBTW(b)
}

func (u *appServerUI) releaseBTW(b *appServerBTW) error {
	method := b.Release()
	if method == "" {
		return nil
	}
	params := map[string]any{"threadId": b.Thread}
	if method == "turn/interrupt" {
		params["turnId"] = b.Turn
	}
	return u.btwRequest(b, method, params)
}

func (u *appServerUI) btwMessage(m appserver.Message, method string, b *appServerBTW) (bool, error) {
	if m.Method == "" {
		if b == nil {
			return false, nil
		}
		u.dirty = true
		if method == "turn/interrupt" {
			b.InterruptAcknowledged()
			if m.Error != nil {
				u.setNotice("Could not interrupt side answer: "+m.Error.Message, true)
			}
			return true, u.releaseBTW(b)
		}
		if method == "thread/unsubscribe" {
			if m.Error != nil {
				u.setNotice("Could not unload side conversation: "+m.Error.Message, true)
			} else {
				u.proxy.setBTWThread(b.Thread, false)
			}
			return true, nil
		}
		b.Starting = false
		if m.Error != nil {
			u.failBTW(b, m.Error.Message)
			if b.Closed {
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
		switch method {
		case "thread/fork":
			if result.Thread.ID == "" || result.Thread.ID == u.Thread {
				u.failBTW(b, "Codex did not return a separate side thread")
				return true, nil
			}
			b.Thread = result.Thread.ID
			// Register before turn/start, so even pre-sampling compaction is
			// rejected before the provider request can run.
			u.proxy.setBTWThread(b.Thread, true)
			u.requests.TrackSide(b.Thread, appServerRequest{side: b})
		case "turn/start":
			if result.Turn.ID == "" {
				u.failBTW(b, "Codex returned no side-turn identity")
			}
			b.StartAcknowledged(result.Turn.ID)
		}
		if b.Closed {
			return true, u.releaseBTW(b)
		}
		return true, u.flushBTW()
	}
	if !u.requests.HasSides() {
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
	b = u.requests.Side(thread).side
	if b == nil {
		return false, nil
	}
	u.dirty = true
	if m.Method == "thread/closed" {
		u.proxy.setBTWThread(thread, false)
		u.requests.CloseSide(thread)
		if !b.Closed {
			b.Thread, b.Turn, b.Busy, b.Starting = "", "", false, false
			b.Status, b.Alert = "Side conversation closed · /btw starts a new branch", true
		}
		return true, nil
	}
	if len(m.ID) != 0 {
		b.Status, b.Alert = "Side answer blocked on "+m.Method+" · Esc closes", true
		return true, nil
	}
	switch m.Method {
	case "turn/started":
		b.Turn = p.Turn.ID
	case "turn/completed":
		if !b.Side.Complete(p.Turn.ID) {
			return true, nil
		}
		if b.Status == conversationmodel.SideCompactionRequired || p.Turn.Error != nil && strings.Contains(p.Turn.Error.Message, conversationmodel.SideCompactionRequired) {
			u.failBTW(b, conversationmodel.SideCompactionRequired)
			return true, nil
		}
		failure := ""
		if p.Turn.Error != nil {
			failure = p.Turn.Error.Message
		}
		b.SideAnswer.Complete(p.Turn.Status, failure)
	case "error":
		if p.Error != nil {
			if strings.Contains(p.Error.Message, conversationmodel.SideCompactionRequired) {
				u.failBTW(b, conversationmodel.SideCompactionRequired)
				return true, nil
			}
			b.Status, b.Alert = p.Error.Message, true
		}
	case "item/agentMessage/delta", "item/completed":
		if b.Closed || p.TurnID != b.Turn {
			return true, nil
		}
		if m.Method == "item/completed" && p.Item.Type != "agentMessage" {
			return true, nil
		}
		id := p.ItemID
		if id == "" {
			id = p.Item.ID
		}
		if m.Method == "item/completed" {
			b.Append(id, p.Item.Text, true)
		} else {
			b.Append(id, p.Delta, false)
		}
	}
	return true, nil
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
