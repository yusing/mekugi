package presentation

import "cmp"

// Source: modules/activity/terminalview/conversation.go:380:405@[a7adb171f] trafficAgent.
// Source: modules/activity/terminalview/conversation.go:662:703@[a7adb171f] agentItem.

// Traffic describes an admitted exchange independently of its renderer's
// glyphs, rails and disclosure geometry. Peer and Reply join local exchanges;
// traffic between other agents keeps its route without joining a local thread.
type Traffic struct {
	Kind      string `json:"kind"`
	From      string `json:"from,omitzero"`
	To        string `json:"to,omitzero"`
	Detail    string `json:"detail,omitzero"`
	Incoming  bool   `json:"incoming,omitzero"`
	Peer      string `json:"-"`
	Reply     bool   `json:"-"`
	External  bool   `json:"-"`
	OwnAnswer bool   `json:"-"`
}

// SameRoute keeps unchanged endpoints implicit in a joined exchange.
func (t *Traffic) SameRoute(other *Traffic) bool {
	return t != nil && other != nil && t.From != "" && t.To != "" && t.From == other.From && t.To == other.To
}

// ReplyAssignment finds the latest retained assignment without quoting it again
// for consecutive answers. Tools, reasoning and progress do not start an exchange.
func ReplyAssignment(entries []Entry, index int, caller, owner string) *Entry {
	entry := entries[index]
	if entry.Question != 0 {
		for i := index - 1; i >= 0; i-- {
			if entries[i].Seq == entry.Question {
				return &entries[i]
			}
		}
		return nil
	}
	// A delivered completion can answer input sent directly to the child.
	// Its receipt in this transcript does not link it to our latest assignment.
	if entry.Delivered {
		return nil
	}
	if len(entry.Values) == 0 {
		return nil
	}
	current := entry.Traffic(entry.Values[0], caller, owner)
	if current == nil || current.Peer == "" {
		return nil
	}
	intervening := false
	for i := index - 1; i >= 0; i-- {
		previous := &entries[i]
		if previous.Suppressed {
			continue
		}
		if (previous.Kind == "start" || previous.Kind == "assignment") && previous.AssignmentTo == current.Peer {
			return previous
		}
		if len(previous.Values) == 0 {
			continue
		}
		traffic := previous.Traffic(previous.Values[0], caller, owner)
		if traffic == nil {
			if previous.Kind == "user" || previous.Agent == "You" || previous.Kind == "text" {
				intervening = true
			}
			continue
		}
		if !intervening && traffic.Kind == "final" && traffic.From == current.From && traffic.To == current.To {
			return nil
		}
		intervening = true
	}
	return nil
}

// Traffic interprets one value, including an exchange within a compound item.
// caller identifies the runtime's Main alias; owner identifies the transcript
// being displayed. OwnAnswer distinguishes an owner's own final answer from a
// received result.
func (e Entry) Traffic(value Value, caller, owner string) *Traffic {
	if e.Kind == "user" || e.Agent == "You" {
		return nil
	}
	identity := func(name string) string {
		from, _ := (Value{From: name}).Endpoints(caller)
		return from
	}
	owner = identity(cmp.Or(owner, caller, "/root"))
	agent := identity(e.Agent)
	from, to := value.Endpoints(caller)
	kind := value.Kind
	if kind == "" || kind == "text" {
		kind = e.Kind
		if kind == "reply" || kind == "assignment" {
			kind = "message"
		}
	}
	t := &Traffic{Kind: kind, From: from, To: to}
	switch kind {
	case "start":
		t.Detail = "started"
		t.To = cmp.Or(to, identity(e.AssignmentTo), agent)
		t.External = from != "" && to != "" && from != owner && to != owner
		if !t.External {
			t.Peer = cmp.Or(value.To, e.Agent)
		}
	case "message":
		t.To = cmp.Or(to, identity(e.AssignmentTo))
		if e.Kind == "assignment" {
			t.Detail = "assignment"
		}
		switch {
		case from == owner:
			t.Peer = cmp.Or(value.To, e.Agent)
		case to == owner:
			t.Peer = cmp.Or(value.From, e.Agent)
		default:
			t.External = from != "" && to != ""
		}
	case "final":
		t.OwnAnswer = agent == owner
		t.From, t.Peer, t.Detail = cmp.Or(from, agent), e.Agent, "finished"
		switch e.Outcome {
		case "interrupted":
			t.Kind, t.Detail = "interrupted", "interrupted"
		case "failed":
			t.Kind, t.Detail = "error", "failed"
		}
	case "error":
		t.From, t.Peer, t.Detail = cmp.Or(from, agent), e.Agent, "failed"
	default:
		return nil
	}
	t.Reply = kind != "start" && e.Kind != "assignment" && (kind != "message" || to == owner)
	t.Incoming = !e.Assignment && agent != owner && (kind == "final" || kind == "message")
	return t
}
