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
	commandPreviews   map[string]*runtimeCommandPreview
	previewBroker     *liveDiffBroker
	previewSubscriber *liveDiffSubscriber
	previewReady      <-chan struct{}
	previewGap        <-chan struct{}
	observations      *ObservationService
	observationEvents <-chan liveDiffEvent
	observationGap    <-chan struct{}
	commandInfo       []session.Command
	models            []session.Model
	settings          *session.Settings
	effortRequest     string
	usage             *session.Usage
	contextTokens     *uint64
	limits            map[string]session.RateLimit
	usagePanel        *appServerStatusReport
	tasks             map[string]session.Task
	taskOrder         []string
	taskCallers       map[string]string
	message           *session.AgentMessage
	messageDraft      composerDraft
	shell             *session.ShellCommand
	shellCapture      *runtimeShellCapture
	stoppingTasks     map[string]bool
	turn              string
	continuation      *journalResetIntent
	continueAt        time.Time
	resetRequest      string
	changeRequest     string
	resetSource       ObservationBinding
	restoredJournal   string
	permissionChoices map[string]string
	skillScan         *runtimeSkillScan
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
	defer u.discardRuntimeShellCapture()
	defer u.closeRuntimeCommandPreviews()
	defer u.finishRuntimeCommandSegments(stdout)
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
				case <-u.runtime.previewReady:
					u.applyRuntimeCommandPreviews()
				case <-u.runtime.previewGap:
					u.applyRuntimeCommandPreviews()
				case <-u.runtime.observationGap:
					u.attachRuntimeObservation(u.runtime.observations)
				case result := <-u.picker.scanResults:
					u.applyPickerScan(result)
				case result := <-u.commandSegmentWrites:
					u.commandSegmentRetained(result)
				case request := <-u.guardRequests():
					u.runtimeGuardApproval(request)
					u.dirty = true
				case key, ok := <-keys:
					if !ok {
						return io.EOF
					}
					if err := u.shell.key(key); err != nil || u.quitRequested {
						return err
					}
					u.dirty = true
				case <-tick.C:
					if u.expireApprovals() {
						u.dirty = true
					}
					if u.expireNotice(u.now()) {
						u.dirty = true
					}
					u.reapRuntimeCommandPreviews()
					u.flushRuntimeCommandSegments()
					if settleActivity(u.now(), u.view, u.agents) {
						u.dirty = true
					}
					for _, view := range []*liveActivityView{u.view, u.agents} {
						if view.pace(u.now()) {
							u.dirty = true
						}
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
					notices := u.applyCriticalNotices()
					w, h, err := term.GetSize(int(stdout.Fd()))
					if err != nil {
						notices.finish(false)
						return err
					}
					if u.dirty || w != width || h != height || u.sessionAnimating() || u.shell.animating(time.Now()) {
						width, height = w, h
						if err := u.paint(stdout, w, h); err != nil {
							notices.finish(false)
							return err
						}
						notices.finish(true)
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

func (u *appServerUI) finishRuntimeCommandSegments(output io.Writer) {
	u.finishCommandSegments()
	// The raw pane has closed before this fallback. A write can fail during
	// the drain, after the last paint, or while Main is hidden in another pane.
	for _, message := range u.issues.Pending() {
		fmt.Fprintln(output, message)
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
		if u.shellMode() {
			return true, false, u.submitShell()
		}
		if u.runtimeMessageCommand(text) {
			return true, false, nil
		}
		if handled, err := u.controlsCommand(strings.TrimSpace(text)); handled {
			return true, false, err
		}
		if u.titleCommand(strings.TrimSpace(text)) {
			return true, false, nil
		}
		if fields := strings.Fields(text); len(fields) > 0 {
			switch fields[0] {
			case "/btw":
				if _, ok := r.client.(session.SideClient); ok {
					if !r.ready || r.settings != nil || r.resetRequest != "" || r.continuation != nil {
						u.setNotice("Wait for native session controls before asking a side question", false)
						return true, false, nil
					}
					return true, false, u.submitBTW()
				}
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
			if r.busy || r.shell != nil {
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
		if r.busy || r.shell != nil {
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
		if r.busy || r.shell != nil {
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
	if e.SideID != "" {
		u.runtimeBTWEvent(e)
		return nil
	}
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
	case "shell_restarting":
		u.closeRuntimeCommandPreviews()
		u.runtime.ready = false
		u.setNotice(e.Text, false)
	case "shell_restarted":
		u.runtime.ready, u.runtime.busy = true, false
		u.runtimeCommands(e.CommandInfo)
		u.runtimeRoster()
		u.status = "Ready"
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
	case "context_usage":
		if !e.Historical && e.Caller == "" && e.AgentID == "" && e.SessionID == u.thread {
			u.runtime.contextTokens = e.ContextTokens
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
		if !e.Historical && e.Task != nil && e.Task.Status == "running" {
			u.confirmRuntimePermission(e.Task.ToolID)
		}
	case "tool_running":
		u.confirmRuntimePermission(e.ID)
	case "agent_message":
		u.runtimeMessageReceipt(e)
	case "shell_started", "shell_done":
		u.runtimeShellEvent(e)
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
		if u.thread != e.SessionID || e.Cwd != "" && u.session.cwd != e.Cwd {
			u.closeRuntimeCommandPreviews()
			u.runtime.contextTokens = nil
		}
		if e.Cwd != "" {
			u.session.cwd, u.shell.diff.workspace = e.Cwd, e.Cwd
		}
		if u.panes != nil && e.SessionID != u.thread {
			u.paneError(u.panes.save(u.shell, u.now(), true))
			u.paneError(u.panes.open(u.shell, u.session.cwd, "claude/"+e.SessionID, true))
		}
		u.thread = e.SessionID
		u.restoreRuntimeHistorySegments()
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
		u.runtimeEntry(session.Event{Kind: "context", ID: "reset/" + e.ID, Text: "Context reset from journal"})
	case "context":
		if e.SessionID != "" && e.SessionID != u.thread {
			return nil
		}
		if e.Caller == "" && e.AgentID == "" && !e.Historical {
			u.runtime.contextTokens = nil
		}
		u.runtimeEntry(e)
	case "skill_history":
		u.runtimeSkillHistory(e)
	case "edit":
		u.runtimePreview(e)
	case "command_preview":
		u.runtimeCommandPreview(e)
	case "message", "tool", "tool_result":
		u.runtimeEntry(e)
		u.flushApprovalUpdates()
		if e.Kind == "tool_result" {
			if !e.Historical {
				u.confirmRuntimePermission(e.ID)
			}
			u.finishRuntimeCommandPreview(e.ID)
			u.settleRuntimePreview(e.ID, e.Failed)
			if e.Historical {
				u.restoreRuntimeCommandOutput(e.ID)
				u.restoreRuntimeCommandSegments(e.ID)
			}
		}
	case "notice":
		u.setNotice(e.Text, false)
		u.runtimeEntry(session.Event{Kind: "notice", Text: e.Text})
	case "error":
		return errors.New(e.Text)
	case "done":
		for id, p := range u.runtime.commandPreviews {
			if !p.complete && p.worker.preview.Caller == "/root" {
				u.finishRuntimeCommandPreview(id)
			}
		}
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
		c := &nativeQuestionCall{thread: u.thread, item: "permission/" + p.ID, prompt: p}
		if len(p.Questions) == 0 {
			u.recordApproval(&nativeApproval{thread: u.thread, item: p.ToolID}, "Pending Approval")
		}
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
	case "permission_decision":
		if e.ID == "" {
			return nil
		}
		outcome := "Approved"
		if e.Failed {
			outcome = "Declined"
		}
		if u.runtime.permissionChoices == nil {
			u.runtime.permissionChoices = make(map[string]string)
		}
		u.runtime.permissionChoices[e.ID] = outcome
		if owner := u.approvalItem(u.thread, "", e.ID); owner != nil && !owner.commandEnded.IsZero() {
			u.confirmRuntimePermission(e.ID)
		}
	case "permission_denied":
		if u.runtime.permissionChoices[e.ID] == "Declined" {
			u.confirmRuntimePermission(e.ID)
			return nil
		}
		owner := u.approvalItem(u.thread, "", e.ID)
		if owner == nil || owner.approval != "Declined" && owner.approval != "Cancelled" {
			u.recordApproval(&nativeApproval{thread: u.thread, item: e.ID}, "Auto Denied")
		}
	case "dismiss":
		for _, c := range u.questions.calls {
			if c.prompt != nil && c.prompt.ID == e.ID && c.questions[0].outcome != "cancelled" {
				delete(u.runtime.permissionChoices, c.prompt.ToolID)
				u.recordApproval(&nativeApproval{thread: u.thread, item: c.prompt.ToolID}, "Cancelled")
				// A sent choice can lose the native cancellation race. Submission
				// is not confirmation that the engine consumed that choice.
				sent := c.sent
				c.resolved, c.sent = false, false
				for i := range c.questions {
					c.questions[i].outcome = ""
					if sent {
						c.questions[i].answer = nil
					}
				}
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
			entry.Agent = u.runtimeCallerLane(e.Caller)
			if entry.Agent == "/root" {
				entry.Agent = "Main"
			}
			if e.Kind == "task" {
				entry.Agent = runtimeTaskLane(strings.TrimPrefix(e.Caller, "task/"))
			}
		}
		if e.Kind == "task" {
			entry.Kind = "progress"
			entry.native = &liveActivityNativeItem{thread: u.thread, item: e.ID, phase: "task"}
		}
		if e.Kind == "context" {
			if e.AgentID != "" {
				entry.Agent = runtimeTaskLane(e.AgentID)
			}
			entry.Kind = "progress"
			entry.native = &liveActivityNativeItem{thread: u.thread, item: e.ID, live: !e.Historical}
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
