package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/session"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"golang.org/x/term"
)

// nativeRuntimeSession is the intent/event boundary for non-app-server hosts.
// It uses the existing shell, composer, question dock and pane controllers.
// No Codex client, proxy, synthetic RPC message or executor is constructed.
type nativeRuntimeSession struct {
	client            session.Client
	name              string
	busy              bool
	ready             bool
	serial            uint64
	previews          map[string]*runtimePreview
	observations      *ObservationService
	observationEvents <-chan liveDiffEvent
	observationGap    <-chan struct{}
	commands          []string
}

func newRuntimeUI(ctx context.Context, client session.Client, name, cwd string) *appServerUI {
	u := &appServerUI{ctx: ctx, view: newLiveActivityView(), agents: newLiveActivityView(), status: "Starting Claude Code…", dirty: true,
		runtime: &nativeRuntimeSession{client: client, name: name}}
	u.session.cwd = cwd
	u.ensureShell()
	u.shell.diff.workspace = cwd
	u.shell.diff.diffMode = false
	u.shell.diff.coverage = "Live proposals only; companion capture is unavailable"
	return u
}

// RunNativeSession presents a native runtime in the same UI used by Codex.
// The caller owns runtime startup/shutdown; this loop owns only the terminal.
func RunNativeSession(ctx context.Context, client session.Client, name, cwd string, stdin, stdout *os.File, observations ...*ObservationService) error {
	u := newRuntimeUI(ctx, client, name, cwd)
	if len(observations) > 0 && observations[0] != nil {
		u.attachRuntimeObservation(observations[0])
	}
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	defer u.cancelPickerScan()
	defer u.discardDraftImages()
	u.shell.faint, _ = terminalui.SupportsFaint(ctx, "auto")
	for {
		err := terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1004;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1004;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
			tick := time.NewTicker(33 * time.Millisecond)
			defer tick.Stop()
			width, height := 0, 0
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case e, ok := <-client.Events():
					if !ok {
						return errors.New("native runtime disconnected; active work may be incomplete")
					}
					if err := u.runtimeEvent(e); err != nil {
						return err
					}
					u.dirty = true
				case event := <-u.runtime.observationEvents:
					u.applyRuntimeObservation(event)
				case <-u.runtime.observationGap:
					u.attachRuntimeObservation(u.runtime.observations)
				case key, ok := <-keys:
					if !ok {
						return io.EOF
					}
					if err := u.shell.key(key); err != nil || u.quitRequested {
						return err
					}
					u.dirty = true
				case <-tick.C:
					if err := u.shell.flushEscape(); err != nil {
						return err
					}
					w, h, err := term.GetSize(int(stdout.Fd()))
					if err != nil {
						return err
					}
					if u.dirty || w != width || h != height || u.sessionAnimating() || u.shell.animating(time.Now()) {
						width, height = w, h
						if err := u.paint(stdout, w, h); err != nil {
							return err
						}
						u.dirty = false
					}
				}
			}
		})
		if !errors.Is(err, errOpenComposerEditor) {
			return err
		}
		u.openComposerEditor(stdin, stdout)
		u.shell.paintedRows = nil
		u.dirty = true
	}
}

// Runtime-specific actions stop here; ordinary editing continues through the
// single existing composer implementation below this boundary.
func (u *appServerUI) runtimeKey(key byte) (handled, quit bool, err error) {
	r := u.runtime
	if u.picker.open {
		return false, false, nil
	}
	if u.escape != "" || key == 27 {
		return false, false, nil
	}
	switch key {
	case '\r', '\t':
		text := u.draft
		if strings.TrimSpace(text) == "" {
			return true, false, nil
		}
		switch strings.TrimSpace(text) {
		case "/quit":
			if r.busy {
				u.setNotice("Interrupt the active turn before quitting", false)
				return true, false, nil
			}
			return true, true, nil
		case "/copy":
			u.showCopyPicker()
			return true, false, nil
		}
		if strings.HasPrefix(strings.TrimSpace(text), "/") && !slices.Contains(r.commands, strings.TrimPrefix(strings.Fields(text)[0], "/")) {
			u.setNotice("This runtime command is not connected yet", true)
			return true, false, nil
		}
		if !r.ready {
			u.setNotice("Runtime is starting · draft kept", false)
			return true, false, nil
		}
		if r.busy {
			u.setNotice("Runtime is working · draft kept · Ctrl-C interrupts", false)
			return true, false, nil
		}
		if err := r.client.Send(u.ctx, text); err != nil {
			return true, false, err
		}
		u.rememberInput(u.draftSnapshot())
		u.loadDraft(composerDraft{})
		u.undoDrafts, u.redoDrafts = nil, nil
		u.historyBack, u.historyDraft, u.run = 0, composerDraft{}, runNone
		r.serial++
		u.runtimeEntry(session.Event{Kind: "message", ID: fmt.Sprintf("input/%d", r.serial), Role: "You", Text: text})
		r.busy = true
		u.status, u.alert = "Working", false
		u.view.follow()
		return true, false, nil
	case 3:
		if u.draft != "" {
			return false, false, nil
		}
		if r.busy {
			u.status = "Interrupting…"
			return true, false, r.client.Interrupt(u.ctx)
		}
		return true, true, nil
	case 22:
		u.setNotice("Image input is not connected for this runtime yet", false)
		return true, false, nil
	}
	return false, false, nil
}

func (u *appServerUI) runtimeEvent(e session.Event) error {
	switch e.Kind {
	case "ready":
		u.runtime.commands = e.Commands
		u.runtime.ready = true
		u.status = "Ready"
	case "session":
		u.thread, u.model = e.SessionID, e.Model
	case "edit":
		u.runtimePreview(e)
	case "message", "tool", "tool_result":
		u.runtimeEntry(e)
		if e.Kind == "tool_result" {
			u.settleRuntimePreview(e.ID, e.Failed)
		}
	case "notice":
		u.setNotice(e.Text, false)
	case "error":
		return errors.New(e.Text)
	case "done":
		for id := range u.runtime.previews {
			u.settleRuntimePreview(id, true)
		}
		u.runtime.busy = false
		if u.runtime.observations != nil && u.runtime.observations.owner.pendingCount.Load() == 0 && !u.shell.diff.modeChosen {
			u.shell.diff.diffMode, u.shell.diff.dirty = true, true
		}
		u.status, u.alert = "Ready", e.Failed
		if e.Failed {
			u.status = "Turn ended"
			u.setNotice(e.Text, true)
		}
	case "prompt":
		if e.Prompt == nil {
			return nil
		}
		p := e.Prompt
		c := &nativeQuestionCall{thread: u.thread, item: p.ID, prompt: p}
		for i, q := range p.Questions {
			n := nativeQuestion{ID: fmt.Sprint(i), Header: q.Header, Question: q.Text, multiple: q.Multiple}
			for _, o := range q.Options {
				n.choices = append(n.choices, nativeQuestionOption{Label: o.Label, Description: o.Description})
			}
			c.questions = append(c.questions, n)
		}
		if len(c.questions) == 0 {
			c.questions = []nativeQuestion{{Header: "Permission · " + p.Tool, Question: p.Description, selected: 1, choices: []nativeQuestionOption{{Label: "Allow once"}, {Label: "Deny"}}}}
		}
		u.questions.calls = append(u.questions.calls, c)
		u.renderQuestionRecord(c)
		if u.questions.active == nil {
			u.openQuestionCall(c)
		}
	case "dismiss":
		for _, c := range u.questions.calls {
			if c.prompt != nil && c.prompt.ID == e.ID {
				u.resolveQuestionCall(c, "cancelled")
			}
		}
		u.openQuestions()
	}
	return nil
}

func (u *appServerUI) runtimeDecision(allow bool) error {
	c := u.questions.active
	if c == nil || c.prompt == nil {
		return nil
	}
	p := c.prompt
	d := session.Decision{ID: p.ID, Allow: allow}
	if len(p.Questions) == 0 {
		d.Allow = allow && c.questions[0].done && !c.questions[0].skipped && c.questions[0].selected == 0
	} else if allow {
		d.Answers = make(map[string]string)
		for i, q := range c.questions {
			if !q.done || q.skipped {
				d.Allow = false
				break
			}
			d.Answers[p.Questions[i].Text] = strings.Join(q.answer, ", ")
		}
	}
	if err := u.runtime.client.Respond(u.ctx, d); err != nil {
		return err
	}
	u.hideQuestions()
	c.sent, c.resolved = true, true
	for i := range c.questions {
		c.questions[i].outcome = "answered"
		if !d.Allow {
			c.questions[i].outcome = "declined"
		}
	}
	u.renderQuestionRecord(c)
	u.openQuestions()
	return nil
}

// Project presentation values directly, never through appServerItem or RPC
// names. Replacement uses native IDs; predicted bytes are not saved edits.
func (u *appServerUI) runtimeEntry(e session.Event) {
	for _, v := range []*liveActivityView{u.view, u.agents} {
		if v == u.agents && e.Kind == "message" {
			continue
		}
		entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: "text", Text: e.Text, CallID: e.ID, Observed: u.now()}
		if e.Caller != "" {
			entry.Agent = "native/" + e.Caller
		}
		if e.Role == "You" {
			entry.Agent = "You"
		}
		var blocks []activityui.Block
		if e.Kind == "tool" || e.Kind == "tool_result" {
			entry.Kind = "runtime_tool"
			label := e.Role
			input := e.Text
			for _, old := range v.entries {
				if old.CallID == e.ID && len(old.blocks) > 0 {
					label = old.blocks[0].Verb
					if e.Kind == "tool_result" {
						input = old.blocks[0].Code
					}
					break
				}
			}
			blocks = []activityui.Block{{Kind: "op", Verb: label, Code: livediff.Safe(input, false), Fenced: true, Running: e.Kind == "tool" && !e.Historical}}
			if e.Kind == "tool_result" {
				blocks[0].Tail = strings.Split(livediff.Safe(e.Text, false), "\n")
			}
			if e.Failed {
				blocks[0].Label = "failed"
			}
		} else {
			blocks = parseLiveActivity(entry)
		}
		found := false
		for i, old := range v.entries {
			if old.CallID == e.ID && e.ID != "" {
				entry.Seq, entry.Observed = old.Seq, old.Observed
				v.replaceEntry(i, entry, blocks)
				found = true
				break
			}
		}
		if !found {
			v.lastSeq = entry.Seq
			v.appendEntry(entry, blocks)
		}
	}
}

// attachRuntimeObservation reuses the durable Diff mailbox and resnapshot path.
// It never restores preview workers, native hooks or running processes.
func (u *appServerUI) attachRuntimeObservation(service *ObservationService) {
	u.runtime.observations = service
	sub := service.owner.broker.subscribe()
	u.runtime.observationEvents, u.runtime.observationGap = sub.events, sub.gap
	u.shell.diff.store = service.owner.store
	u.shell.diff.allowModeSwitch = true
	u.shell.diff.coverage = "Companion observations · call-window evidence"
}

func (u *appServerUI) applyRuntimeObservation(event liveDiffEvent) {
	if _, err := u.shell.diff.applyEvent(u.ctx, event); err != nil {
		u.shell.diffFailure = err.Error()
	} else {
		u.shell.diff.coverage = "Companion observations · call-window evidence"
		u.runtimeCapturedEdits()
		// The existing controller preserves navigator/follow anchors on Merge.
		if !u.runtime.busy && u.runtime.observations.owner.pendingCount.Load() == 0 && !u.shell.diff.modeChosen {
			u.shell.diff.diffMode, u.shell.diff.dirty = true, true
		}
	}
	u.dirty = true
}

func (u *appServerUI) runtimeCapturedEdits() {
	for _, key := range u.shell.diff.data.order {
		receipt := u.shell.diff.data.attempts[key].receipt
		if receipt == nil || receipt.runtime == "" {
			continue
		}
		// A capture card is separate evidence, not a rewritten native tool result.
		u.runtimeEntry(session.Event{Kind: "capture", ID: "capture/" + key, Role: "Capture", Caller: receipt.agent,
			Text: "Captured " + receipt.change + "\n\n" + receipt.text, Historical: true})
	}
}
