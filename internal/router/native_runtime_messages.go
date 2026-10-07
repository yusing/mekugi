package router

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/session"
)

// /to uses the ordinary composer and completion viewport. A roster target is
// a selector only; native admission checks live or saved child ownership.
func (u *appServerUI) runtimeAgentTarget() composerTarget {
	if u.runtime == nil {
		return composerTarget{}
	}
	if _, ok := u.runtime.client.(session.AgentMessageClient); !ok {
		return composerTarget{}
	}
	if !strings.HasPrefix(u.draft, "/to") || len(u.draft) <= 3 {
		return composerTarget{}
	}
	first, _ := utf8.DecodeRuneInString(u.draft[3:])
	if !unicode.IsSpace(first) {
		return composerTarget{}
	}
	start := 3
	for start < len(u.draft) {
		r, n := utf8.DecodeRuneInString(u.draft[start:])
		if !unicode.IsSpace(r) {
			break
		}
		start += n
	}
	end := start
	for end < len(u.draft) {
		r, n := utf8.DecodeRuneInString(u.draft[end:])
		if unicode.IsSpace(r) {
			break
		}
		end += n
	}
	if at := u.cursor(); at < start || at > end {
		return composerTarget{}
	}
	return composerTarget{kind: 't', start: start, end: end, query: u.draft[start:end]}
}

func (u *appServerUI) runtimeAgentChoices(query string) {
	p := &u.picker
	previous := ""
	if p.selected < len(p.choices) {
		previous = p.choices[p.selected].name
	}
	p.choices, p.loading, p.problem = nil, false, ""
	query = strings.TrimPrefix(query, "/root/")
	for _, id := range u.runtime.taskOrder {
		task := u.runtime.tasks[id]
		if task.Kind != "local_agent" || task.Ambient {
			continue
		}
		if _, ok := pickerMatchScore(id, query); !ok {
			continue
		}
		name := runtimeTaskLane(id)
		p.choices = append(p.choices, composerChoice{name: name, path: name, description: strings.TrimSpace(task.Role + " " + task.Description + " " + task.Status)})
	}
	slices.SortStableFunc(p.choices, func(a, b composerChoice) int {
		as, _ := pickerMatchScore(strings.TrimPrefix(a.name, "/root/"), query)
		bs, _ := pickerMatchScore(strings.TrimPrefix(b.name, "/root/"), query)
		return as - bs
	})
	p.selected = max(0, slices.IndexFunc(p.choices, func(c composerChoice) bool { return c.name == previous }))
	if len(p.choices) == 0 && query == "" {
		p.problem = "No native child targets"
	}
}

func (u *appServerUI) runtimeMessageCommand(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] != "/to" {
		return false
	}
	r := u.runtime
	client, ok := r.client.(session.AgentMessageClient)
	if !ok {
		u.setNotice("Direct native child messages are unavailable", true)
		return true
	}
	if !r.ready || r.changeRequest != "" || r.resetRequest != "" || r.message != nil {
		u.setNotice("Wait for native session or message delivery · draft kept", false)
		return true
	}
	if len(fields) < 3 {
		u.setNotice("Use /to <agent> <message> · draft kept", true)
		return true
	}
	if len(u.images) > 0 || len(u.files) > 0 || len(u.selections) > 0 {
		u.setNotice("Native child messages accept plain text · attachments kept", true)
		return true
	}
	// Remove only the command and target separators; retain message whitespace.
	body := strings.TrimLeftFunc(text, unicode.IsSpace)
	body = strings.TrimLeftFunc(strings.TrimPrefix(body, "/to"), unicode.IsSpace)
	body = strings.TrimLeftFunc(strings.TrimPrefix(body, fields[1]), unicode.IsSpace)
	r.serial++
	message := session.AgentMessage{ID: fmt.Sprintf("message/%d", r.serial), SessionID: u.thread, AgentID: strings.TrimPrefix(fields[1], "/root/"), Text: body}
	if err := client.SendAgentMessage(u.ctx, message); err != nil {
		u.setNotice("Native child message not sent: "+err.Error()+" · draft kept", true)
		return true
	}
	r.message, r.messageDraft = &message, u.draftSnapshot()
	u.rememberInput(r.messageDraft)
	u.setNotice("Delivering native child message…", false)
	return true
}

func (u *appServerUI) runtimeMessageReceipt(e session.Event) {
	r := u.runtime
	m := e.AgentMessage
	if m == nil || r.message == nil || m.ID != r.message.ID || m.SessionID != r.message.SessionID || m.AgentID != r.message.AgentID {
		return
	}
	draft := r.messageDraft
	r.message, r.messageDraft = nil, composerDraft{}
	if e.Failed {
		u.setNotice("Native child message unavailable: "+m.Text+" · draft kept · Up recalls sent text", true)
		return
	}
	if u.draft == draft.text && len(u.images) == 0 && len(u.files) == 0 && len(u.selections) == 0 {
		u.loadDraft(composerDraft{})
		u.undoDrafts, u.redoDrafts = nil, nil
		u.historyBack, u.historyDraft, u.run = 0, composerDraft{}, runNone
	}
	lane := runtimeTaskLane(m.AgentID)
	for _, v := range []*liveActivityView{u.view, u.agents} {
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: v.lastSeq + 1, Agent: lane, Kind: "reply", CallID: m.ID, Text: m.Text, Observed: u.now(), message: &activityMessage{from: "You via Mekugi", to: lane, text: m.Text}}}})
	}
	u.setNotice("Native child message queued · "+lane, false)
}
