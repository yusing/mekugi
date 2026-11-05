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
