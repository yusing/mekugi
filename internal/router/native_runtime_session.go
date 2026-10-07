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

	"github.com/yusing/mekugi/internal/session"
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
	commandInfo       []session.Command
	models            []session.Model
	settings          *session.Settings
	effortRequest     string
	usage             *session.Usage
	limits            map[string]session.RateLimit
	usagePanel        *appServerStatusReport
	tasks             map[string]session.Task
	taskOrder         []string
	stoppingTasks     map[string]bool
	turn              string
	continuation      *journalResetIntent
	continueAt        time.Time
	resetRequest      string
	changeRequest     string
	resetSource       ObservationBinding
	restoredJournal   string
}

func newRuntimeUI(ctx context.Context, client session.Client, name, cwd string) *appServerUI {
	u := &appServerUI{ctx: ctx, view: newLiveActivityView(), agents: newLiveActivityView(), status: "Starting Claude Code…", dirty: true,
		runtime: &nativeRuntimeSession{client: client, name: name}}
	u.view.clock, u.agents.clock = u.now, u.now
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
	u.panes = &nativePanePersistence{}
	defer func() { u.paneError(u.panes.save(u.shell, u.now(), true)) }()
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
				case result := <-u.picker.scanResults:
					u.applyPickerScan(result)
				case key, ok := <-keys:
					if !ok {
						return io.EOF
					}
					if err := u.shell.key(key); err != nil || u.quitRequested {
						return err
					}
					u.dirty = true
				case <-tick.C:
					u.flushRuntimeCommandSegments()
					if settleActivity(u.now(), u.view, u.agents) {
						u.dirty = true
					}
					if s := u.runtime.observations; s != nil && s.journal != nil && u.runtime.changeRequest == "" {
						if sink := s.journal.sink(); sink != nil {
							u.journal = sink
						}
					}
					pending := u.runtimeJournalPending()
					if err := u.tickRuntimeJournal(u.now()); err != nil {
						u.setNotice("Journal continuation unavailable: "+err.Error(), true)
					}
					if err := u.drainKeys(keys); err != nil || u.quitRequested {
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
						if err := u.acknowledgeRuntimeJournal(pending); err != nil {
							u.setNotice("Journal paint receipt unavailable: "+err.Error(), true)
						}
						u.paneError(u.panes.save(u.shell, u.now(), false))
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

func (u *appServerUI) runtimeJournalPending() []nativeJournalPublication {
	if u.journal == nil {
		return nil
	}
	items := u.journal.snapshot()
	for _, item := range items {
		u.applyJournalPublication(u.journal, item)
	}
	u.dirty = u.dirty || len(items) > 0
	return items
}

func (u *appServerUI) acknowledgeRuntimeJournal(items []nativeJournalPublication) error {
	s := u.runtime.observations
	if s == nil || s.journal == nil || u.journal == nil {
		return nil
	}
	if !u.mainContentPainted {
		if !u.journalPanePresents(u.journal) {
			return nil
		}
		items = slices.DeleteFunc(slices.Clone(items), func(p nativeJournalPublication) bool { return p.card != nil || p.event == nil })
	}
	ctx, err := s.journal.scope(u.ctx, s.journal.rootBinding())
	if err != nil {
		return err
	}
	return u.journal.acknowledgeOwned(ctx, s.journal.journals, s.owner.store, items)
}

// Runtime-specific actions stop here; ordinary editing continues through the
// single existing composer implementation below this boundary.
func (u *appServerUI) runtimeKey(key byte) (handled, quit bool, err error) {
	r := u.runtime
	if (r.continuation != nil || r.resetRequest != "") && key == 3 && u.escape == "" {
		return true, false, u.cancelRuntimeContinuation(true)
	}
	// Enter on a native slash completion must dispatch through this backend,
	// not fall through to Codex's command handlers after the picker closes.
	if key == '\r' && u.escape == "" && u.picker.open && u.statusPanel == nil {
		if u.pickerKey("\r") {
			return true, false, nil
		}
	}
	if u.picker.open || u.statusPanel != nil {
		return false, false, nil
	}
	if u.escape != "" || key == 27 {
		return false, false, nil
	}
	switch key {
	case '\r', '\t':
		text := u.draft
		if handled, err := u.controlsCommand(strings.TrimSpace(text)); handled {
			return true, false, err
		}
		if u.titleCommand(strings.TrimSpace(text)) {
			return true, false, nil
		}
		if fields := strings.Fields(text); len(fields) > 0 {
			switch fields[0] {
			case "/resume":
				return true, false, u.resumeCommand(text)
			case "/clear":
				return true, false, u.sessionCommand("/clear")
			}
		}
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
		case "/usage", "/session", "/status":
			u.showRuntimeUsage()
			return true, false, nil
		}
		if fields := strings.Fields(text); len(fields) > 0 && fields[0] == "/live" {
			handled, err := u.settingsCommand(text)
			return handled, false, err
		}
		if handled, err := u.runtimeSettingsCommand(text); handled {
			return true, false, err
		}
		if strings.HasPrefix(strings.TrimSpace(text), "/") && !slices.ContainsFunc(r.commandInfo, func(c session.Command) bool {
			return c.Name == strings.TrimPrefix(strings.Fields(text)[0], "/")
		}) {
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
		if r.settings != nil {
			u.setNotice("Settings update pending · draft kept", false)
			return true, false, nil
		}
		if strings.TrimSpace(text) == "/compact" && u.runtimeJournalOwner() != nil {
			if _, ok := r.client.(session.ResetClient); ok {
				if err := u.cancelRuntimeContinuation(false); err != nil {
					return true, false, err
				}
				if err := u.requestRuntimeReset(); err != nil {
					u.setNotice("Journal reset unavailable: "+err.Error(), true)
					return true, false, nil
				}
				u.loadDraft(composerDraft{})
				return true, false, nil
			}
		}
		if err := u.cancelRuntimeContinuation(false); err != nil {
			return true, false, err
		}
		if err := u.beginRuntimeJournalTurn(); err != nil {
			u.setNotice("Journal turn unavailable: "+err.Error(), true)
			return true, false, nil
		}
		if err := u.sendRuntimeInput(); err != nil {
			u.setNotice("Input not sent: "+err.Error()+" · draft kept", true)
			return true, false, nil
		}
		u.rememberInput(u.draftSnapshot())
		u.loadDraft(composerDraft{})
		u.undoDrafts, u.redoDrafts = nil, nil
		u.historyBack, u.historyDraft, u.run = 0, composerDraft{}, runNone
		r.serial++
		u.runtimeEntry(session.Event{Kind: "message", ID: fmt.Sprintf("input/%d", r.serial), Role: "You", Text: text})
		r.busy = true
		u.runtimeRoster()
		u.status, u.alert = "Working", false
		u.view.follow()
		return true, false, nil
	case 3:
		if u.draft != "" {
			return false, false, nil
		}
		if r.busy {
			return true, false, u.keyboardInterrupt()
		}
		if u.interruptLocked {
			u.lockNotice()
			return true, false, nil
		}
		return true, true, nil
	case 22:
		if _, ok := r.client.(session.InputClient); ok {
			u.pasteImage()
		} else {
			u.setNotice("Image input is unavailable for this runtime", false)
		}
		return true, false, nil
	}
	return false, false, nil
}

func (u *appServerUI) runtimeEvent(e session.Event) error {
	switch e.Kind {
	case "ready":
		u.runtimeCommands(e.CommandInfo)
		u.runtime.models = e.Models
		u.runtime.ready = true
		u.status = "Ready"
		u.runtimeRoster()
	case "commands":
		u.runtimeCommands(e.CommandInfo)
		u.refreshRuntimePicker()
	case "settings":
		u.runtimeSettingsReceipt(e)
	case "title":
		if e.Title != nil {
			failure := ""
			if e.Failed {
				failure = e.Text
				if failure == "" {
					failure = "Native rename failed"
				}
			}
			u.finishSessionTitle(e.Title.ID, failure)
		}
	case "sessions":
		return u.runtimeSessionPage(e)
	case "session_change", "session_ready":
		return u.runtimeSessionChanged(e)
	case "usage":
		if e.Usage != nil {
			u.runtime.usage = e.Usage
			u.renderRuntimeUsage()
			u.runtimeRoster()
		}
	case "limit":
		if e.Limit != nil {
			if u.runtime.limits == nil {
				u.runtime.limits = make(map[string]session.RateLimit)
			}
			u.runtime.limits[e.Limit.Window+"/"+e.Limit.Scope] = *e.Limit
			u.renderRuntimeUsage()
		}
	case "task":
		u.runtimeTask(e)
	case "command_output":
		u.runtimeCommandOutput(e)
	case "task_control":
		if u.runtime.stoppingTasks[e.ID] {
			delete(u.runtime.stoppingTasks, e.ID)
			u.setNotice("Native task stop requested", false)
			if e.Failed {
				u.setNotice("Native task stop failed: "+e.Text, true)
			}
		}
	case "session":
		if e.Cwd != "" {
			u.session.cwd, u.shell.diff.workspace = e.Cwd, e.Cwd
		}
		if u.panes != nil && e.SessionID != u.thread {
			u.paneError(u.panes.save(u.shell, u.now(), true))
			u.paneError(u.panes.open(u.shell, u.session.cwd, "claude/"+e.SessionID, true))
		}
		u.thread = e.SessionID
		if e.Title != nil {
			u.confirmSessionTitle(e.SessionID, e.Title.Title)
		}
		if u.pendingTitle != "" {
			name := u.pendingTitle
			u.pendingTitle = ""
			u.renameSessionTitle(name)
		}
		if e.Model != "" {
			u.model = e.Model
		}
	case "reset_ready":
		return u.runtimeResetReady(e)
	case "reset":
		if u.runtime.observations != nil {
			u.attachRuntimeObservation(u.runtime.observations)
		}
		u.setNotice("Context reset from journal · native session "+e.SessionID, false)
	case "edit":
		u.runtimePreview(e)
	case "message", "tool", "tool_result":
		u.runtimeEntry(e)
		if e.Kind == "tool_result" {
			u.settleRuntimePreview(e.ID, e.Failed)
		}
	case "notice":
		u.setNotice(e.Text, false)
		u.runtimeEntry(session.Event{Kind: "notice", Text: e.Text})
	case "error":
		return errors.New(e.Text)
	case "done":
		if s := u.runtime.observations; s != nil && s.journal != nil {
			if err := s.journal.complete(u.ctx, e.ID); err != nil {
				u.setNotice("Journal completion report unavailable: "+err.Error(), true)
			}
		}
		for id := range u.runtime.previews {
			u.settleRuntimePreview(id, true)
		}
		u.runtime.busy = false
		u.runtimeRoster()
		if u.runtime.observations != nil && u.runtime.observations.owner.pendingCount.Load() == 0 && !u.shell.diff.modeChosen {
			u.shell.diff.diffMode, u.shell.diff.dirty = true, true
		}
		u.status, u.alert = "Ready", e.Failed
		if e.Failed {
			u.status = "Turn ended"
			u.setNotice(e.Text, true)
		} else if err := u.completedRuntimeJournal(); err != nil {
			u.setNotice("Journal continuation unavailable: "+err.Error(), true)
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
	var tool activityPaneEntry
	if e.Kind == "tool" || e.Kind == "tool_result" {
		tool = u.runtimeToolEntry(u.view, activityPaneEntry{Observed: u.now()}, e)
	}
	for _, v := range []*liveActivityView{u.view, u.agents} {
		entry := activityPaneEntry{Seq: v.lastSeq + 1, Agent: "Main", Kind: "text", Text: e.Text, CallID: e.ID, Observed: u.now()}
		if e.Caller != "" {
			entry.Agent = "native/" + e.Caller
			if e.Kind == "task" {
				entry.Agent = runtimeTaskLane(strings.TrimPrefix(e.Caller, "task/"))
			}
			for _, id := range u.runtime.taskOrder {
				if task := u.runtime.tasks[id]; task.ToolID == e.Caller {
					if task.Kind == "local_bash" {
						entry.Agent = "Main"
					} else {
						entry.Agent = runtimeTaskLane(id)
					}
					break
				}
			}
		}
		if e.Kind == "task" {
			entry.Kind = "progress"
			entry.native = &liveActivityNativeItem{thread: u.thread, item: e.ID, phase: "task"}
		}
		if e.Role == "You" {
			entry.Agent = "You"
		}
		if v == u.agents && e.Kind == "message" && e.Caller == "" {
			v.lastSeq = entry.Seq
			v.observeStandalone(entry)
			continue
		}
		if e.Kind == "tool" || e.Kind == "tool_result" {
			entry.Kind, entry.Text, entry.native = tool.Kind, tool.Text, tool.native
			entry.outputTail, entry.outputOmit = tool.outputTail, tool.outputOmit
		}
		blocks := parseLiveActivity(entry)
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
			v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
		}
	}
}

// attachRuntimeObservation reuses the durable Diff mailbox and resnapshot path.
// It never restores preview workers, native hooks or running processes.
func (u *appServerUI) attachRuntimeObservation(service *ObservationService) {
	u.runtime.observations = service
	u.execTrack = service.owner.execTrack
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
