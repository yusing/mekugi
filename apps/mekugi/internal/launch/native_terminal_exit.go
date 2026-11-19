package launch

import (
	"context"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/completion"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

type nativeExitAction uint8

const (
	exitOffer nativeExitAction = iota
	exitContinue
	exitStop
	exitDeferred
)

const (
	exitContinueChoice = 1
	exitStopChoice     = 2
)

// In-flight exit is a decision on the existing attachment and host family.
// Work observed at the prompt can finish before Stop, but not be replaced by
// different work that the user never authorized us to stop.
type nativeTerminalExit struct {
	action          nativeExitAction
	attachment      string
	thread, sampled string
	offered         []nativeExitWork
}

type nativeExitWork struct {
	owner  string
	target sessioncontrol.InterruptTarget
	state  string
	at     int64
}

func (c *nativeConnection) checkTerminalExit(endpoint *nativeEndpoint, view *nativeSessionView, action nativeExitAction, offered []nativeExitWork) (string, error) {
	attachment := view.attached.view
	if endpoint.exit != nil || endpoint.ending || view.picker.Kind == sessionpresentation.MenuExit {
		return "A session exit is already being decided.", nil
	}
	if endpoint.api.Native().HasOtherViews(attachment) {
		if action == exitStop {
			return "Another view attached; the session was not stopped.", nil
		}
		endpoint.api.Native().CloseView(attachment)
		return "", nil
	}
	thread := endpoint.session.Client.Thread
	root := c.exitRoot(endpoint)
	if root == "" {
		return "The session changed before exit.", nil
	}
	c.invalidateTree(root)
	endpoint.exit = &nativeTerminalExit{action: action, attachment: attachment, thread: thread, sampled: c.families[root].response, offered: offered}
	if err := c.observeRoot(root); err != nil {
		endpoint.exit = nil
		return fmt.Sprintf("Could not check running work: %v", err), nil
	}
	return "", nil
}

func (c *nativeConnection) chooseTerminalExit(endpoint *nativeEndpoint, view *nativeSessionView, request sessionapi.NativeRequest) (string, error) {
	action, err := view.menuAction(request)
	if err != nil {
		return err.Error(), nil
	}
	work := view.exitWork
	view.exitWork = nil
	view.picker.Open, view.picker.Kind, view.picker.Choices = false, "", nil
	if action.Kind != sessionpresentation.MenuAccept {
		return "", nil
	}
	if action.Choice.ID == exitStopChoice {
		return c.checkTerminalExit(endpoint, view, exitStop, work)
	}
	return c.checkTerminalExit(endpoint, view, exitContinue, nil)
}

func (c *nativeConnection) exitRoot(endpoint *nativeEndpoint) string {
	if endpoint.session == c.root {
		return endpoint.session.Client.Thread
	}
	return endpoint.session.Client.NativeRoot
}

func (c *nativeConnection) advanceTerminalExit(endpoint *nativeEndpoint) error {
	exit := endpoint.exit
	if exit == nil {
		return nil
	}
	view := endpoint.views.views[exit.attachment]
	if exit.thread != endpoint.session.Client.Thread || exit.action != exitDeferred && (view == nil || view.ctx.Err() != nil) {
		endpoint.exit = nil
		return nil
	}
	family := c.families[c.exitRoot(endpoint)]
	if family == nil || family.pending || family.dirty {
		return nil
	}
	if family.response == "" || family.response == exit.sampled {
		endpoint.exit = nil
		if exit.action == exitStop {
			setNotice(&view.attached.feedback, presentationTime(endpoint.session.clock), "Could not confirm running work; the session was not stopped.", true)
		} else {
			endpoint.api.Native().CloseView(exit.attachment)
		}
		return nil
	}
	targetBusy, blocked, work, descendants := c.terminalExitWork(endpoint, family)
	endpoint.exit = nil
	if blocked {
		// A replaced Main can still share the host with work in a different
		// family. Stopping this host would stop more than the selected session.
		if exit.action == exitStop {
			setNotice(&view.attached.feedback, presentationTime(endpoint.session.clock), "Other sessions are still using this host; cannot stop only this session.", true)
			return nil
		}
		if !targetBusy {
			if exit.action != exitDeferred {
				endpoint.api.Native().CloseView(exit.attachment)
			}
			exit.action = exitDeferred
			endpoint.exit = exit
		} else if exit.action != exitDeferred {
			endpoint.api.Native().CloseView(exit.attachment)
		}
		return nil
	}
	if len(work) != 0 {
		switch exit.action {
		case exitOffer:
			if slices.ContainsFunc(descendants, func(child *sessionapi.Native) bool { return child.HasOtherViews("") }) {
				endpoint.api.Native().CloseView(exit.attachment)
				return nil
			}
			view.offerTerminalExit(work)
			return nil
		case exitContinue, exitDeferred:
			if !targetBusy {
				if exit.action == exitContinue {
					endpoint.api.Native().CloseView(exit.attachment)
				}
				exit.action = exitDeferred
				endpoint.exit = exit
			} else if exit.action == exitContinue {
				endpoint.api.Native().CloseView(exit.attachment)
			}
			return nil
		case exitStop:
			if slices.ContainsFunc(work, func(current nativeExitWork) bool { return !slices.Contains(exit.offered, current) }) {
				view.offerTerminalExit(work)
				return nil
			}
		}
	}
	scope := descendants
	if endpoint.session != c.root && (exit.action != exitStop || len(work) == 0) {
		// An idle delegate is released through native idle eviction, not the
		// explicit subtree stop. Descendant endpoints stay registered.
		scope = nil
	}
	if !endpoint.api.Native().BeginRelease(exit.attachment, scope) {
		if exit.action == exitStop && len(work) != 0 {
			setNotice(&view.attached.feedback, presentationTime(endpoint.session.clock), "Another view attached; the session was not stopped.", true)
		} else {
			endpoint.api.Native().CloseView(exit.attachment)
		}
		return nil
	}
	if endpoint.session == c.root {
		c.root.quitRequested = true
		return nil
	}
	fail := func(err error) {
		if err != nil {
			endpoint.api.Native().CancelRelease()
			for _, child := range scope {
				child.CancelRelease()
			}
			if view != nil && view.ctx.Err() == nil {
				setNotice(&view.attached.feedback, presentationTime(endpoint.session.clock), err.Error(), true)
			}
		}
	}
	if exit.action == exitStop && len(work) != 0 {
		c.endEndpoint(&nativeEndRequest{ctx: context.WithoutCancel(view.ctx), thread: exit.thread, onDone: fail})
		return nil
	}
	_, err := c.root.Client.Requests.Send("thread/unsubscribe", map[string]any{"threadId": exit.thread}, "native/idle-release", appServerRequest{nativeIdle: endpoint})
	if err != nil {
		fail(err)
	}
	return nil
}

func (v *nativeSessionView) offerTerminalExit(work []nativeExitWork) {
	v.exitWork = work
	v.picker.openMenu(string(sessionpresentation.MenuExit))
	v.picker.Choices = []completion.Choice{
		{ID: exitContinueChoice, Name: "Continue in background"},
		{ID: exitStopChoice, Name: "Stop"},
	}
}

func (c *nativeConnection) terminalExitWork(endpoint *nativeEndpoint, family *nativeFamily) (targetBusy, blocked bool, work []nativeExitWork, descendants []*sessionapi.Native) {
	target := endpoint.session.Client.Thread
	parents := make(map[string]string, len(family.threads))
	for _, agent := range family.threads {
		parents[agent.ID] = agent.ParentThreadID
	}
	owns := func(thread string) bool {
		for range len(parents) + 1 {
			if thread == target {
				return true
			}
			if thread = parents[thread]; thread == "" {
				break
			}
		}
		return false
	}
	add := func(fact nativeExitWork) {
		if owns(fact.owner) {
			work = append(work, fact)
			targetBusy = targetBusy || fact.owner == target
		} else if endpoint.session == c.root {
			blocked = true
		}
	}
	for _, agent := range family.threads {
		// A fresh or restored Main starts with PendingInit even when it
		// has no turn. PendingInit does represent work for spawned agents.
		if state := agent.AgentState(); state == "running" || state == "pending_init" && agent.ParentThreadID != "" {
			known := c.byThread[agent.ID]
			if known == nil || known.session.Client.Live.Turn == "" {
				add(nativeExitWork{owner: agent.ID, state: state, at: agent.UpdatedAt})
			}
		}
	}
	if endpoint.session == c.root {
		for _, other := range c.families {
			if other == family {
				continue
			}
			for _, agent := range other.threads {
				state := agent.AgentState()
				blocked = blocked || agent.ParentThreadID != "" && (state == "running" || state == "pending_init")
			}
		}
	}
	for _, other := range c.endpoints {
		session := other.session
		thread := session.Client.Thread
		if thread == "" && session.nativeAgent != nil {
			thread = session.nativeAgent.ID
		}
		if other != endpoint && (endpoint.session == c.root || owns(thread)) {
			descendants = append(descendants, other.api.Native())
		}
		control := &session.Client.Live
		// NativeInputPending is a capability gate (also set on retained-only
		// threads), not evidence of an admitted turn.
		if sessionBusy(control, session.attached.conversation) || control.Replacement.Pending() {
			add(nativeExitWork{owner: thread, target: control.InterruptTarget()})
		}
		for _, waiting := range [2][]conversationmodel.Input{session.attached.conversation.Unsent, session.attached.conversation.Queued} {
			for _, input := range waiting {
				add(nativeExitWork{owner: thread, state: input.Origin, at: int64(input.Delivery)})
			}
		}
		for _, side := range session.Client.Requests.OwnedSides() {
			if side.Busy || side.Starting || side.ForkPending {
				add(nativeExitWork{owner: thread, target: side.InterruptTarget(), state: side.Key})
			}
		}
	}
	return targetBusy, blocked, work, descendants
}

func (c *nativeConnection) releasedTerminalChild(endpoint *nativeEndpoint, message appserver.Message) {
	if message.Error != nil {
		endpoint.api.Native().CancelRelease()
		setNotice(&endpoint.session.attached.feedback, presentationTime(endpoint.session.clock), "Could not release idle delegate: "+message.Error.Message, true)
		return
	}
	if c.byThread[endpoint.session.Client.Thread] == endpoint && endpoint.session != c.root {
		c.retire(endpoint, nil)
	}
}
