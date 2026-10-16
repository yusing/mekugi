package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// appServerLifecycle joins sessioncontrol's compaction workflow with the remaining
// composer and host-operation records. RPC acknowledgement, turn identity and
// committed input are separate observations when admitting the next input.
type appServerLifecycle struct {
	thread, turn string
	appServerInputOperation
	appServerResumeOperation
	compaction   sessioncontrol.Compaction
	interruption appServerInterruptOperation
	shellCommand appServerShellOperation
	settings     appServerSettingsOperation
	replacement  appServerReplacementOperation
}

func (h *appServerLifecycle) starting() bool {
	return h.awaitingTurn || h.compaction.Starting() || h.shellCommand.phase == shellStarting
}

func (h *appServerLifecycle) started(turn string) {
	h.turn, h.awaitingTurn = turn, false
	h.compaction.Started(turn)
	h.shellCommand.started(turn)
}

func (h *appServerLifecycle) completed(success bool) {
	h.compaction.Completed(h.turn, success)
	h.turn, h.awaitingTurn = "", false
	h.shellCommand.completed()
	// Input settlement still owns restoring uncommitted input to the composer.
	h.interruption.finish()
}

func (h *appServerLifecycle) compactResponse(failed bool) {
	h.compaction.Acknowledge(failed)
	if failed {
		h.awaitingTurn = false
		h.interruption.cancelDeferred()
	}
}

// acceptsInput is the host gate. Questions and history restoration
// retain their own owners and are checked by the UI before this gate.
func (h *appServerLifecycle) acceptsInput() bool {
	return h.thread != "" && !h.replacement.pending() && !h.starting() &&
		!h.appServerInputOperation.pending() && !h.steerInterruptAckPending && !h.compaction.Pending() &&
		!h.settings.blocksInput() && !h.shellCommand.blocksInput(h.turn) &&
		(h.turn == "" || h.turn != h.interruption.target)
}

func (h *appServerLifecycle) acceptsShell() bool {
	return !h.replacement.pending() && h.shellCommand.pending.text == "" &&
		h.shellCommand.origin == nil && !h.starting() && !h.compaction.Pending() &&
		!h.appServerInputOperation.pending() && h.interruption.target == "" &&
		!h.steerInterruptAckPending
}

func (h *appServerLifecycle) busy() bool {
	return h.turn != "" || h.starting() || h.compaction.Pending() ||
		h.appServerInputOperation.pending() || h.settings.pending() ||
		h.shellCommand.pending.text != "" || len(h.unsent)+len(h.queued) > 0
}

// leaveThread retires host operations as one unit. Queued input belongs to the
// composer and follows the confirmed replacement. Target settings intent is
// installed separately, only after the new host identity has been confirmed.
func (h *appServerLifecycle) leaveThread() {
	thread, unsent, queued := h.thread, h.unsent, h.queued
	resume := h.appServerResumeOperation
	resume.resumeThread, resume.resumeCwd = "", ""
	*h = appServerLifecycle{thread: thread, unsent: unsent, queued: queued, appServerResumeOperation: resume}
}

// Resume intent belongs to the target; until confirmation it must not replace
// the source thread's settings gate. Buffered observations are replayed once
// history restoration completes, never used to revive process resources.
type appServerResumeOperation struct {
	resumeThread        string
	resumeCwd           string
	resumeConfig        map[string]any
	resumePendingEffort bool
	resumeEvidence      preferences.Evidence
	resumePending       []appserver.Message
}

func (o *appServerResumeOperation) takeDefaultEffort() bool {
	needed := o.resumePendingEffort
	o.resumePendingEffort = false
	return needed
}

func (o *appServerResumeOperation) takeEvents() []appserver.Message {
	pending := o.resumePending
	o.resumePending = nil
	return pending
}

type appServerInputOperation struct {
	submission               composerSubmission // RPC acknowledgement outstanding.
	pendingStart             composerSubmission // Committed user message outstanding.
	steers                   []composerSubmission
	unsent, queued           []composerDraft
	awaitingTurn             bool
	sendSteersAfterInterrupt bool // Escape expedites uncommitted steers; Ctrl-C restores them.
	steerInterruptAckPending bool // Delivery waits for the host's interrupt acknowledgement too.
}

func (o *appServerInputOperation) pending() bool { return o.submission.text != "" }

func (o *appServerInputOperation) submit(s composerSubmission) {
	o.submission = s
	if s.turn == "" {
		o.pendingStart, o.awaitingTurn = s, true
	}
}

func (o *appServerInputOperation) acknowledge() composerSubmission {
	s := o.submission
	o.submission = composerSubmission{}
	return s
}

func (o *appServerInputOperation) withdrawStart() {
	o.awaitingTurn = false
	o.pendingStart = composerSubmission{}
}

type appServerInterruptOperation struct {
	target      string
	beforeStart bool
}

func (o *appServerInterruptOperation) deferUntilStart() { o.beforeStart = true }
func (o *appServerInterruptOperation) cancelDeferred()  { o.beforeStart = false }
func (o *appServerInterruptOperation) begin(turn string) bool {
	if o.target == turn {
		return false
	}
	o.target = turn
	return true
}
func (o *appServerInterruptOperation) finish() { *o = appServerInterruptOperation{} }

type shellPhase uint8

const (
	shellIdle shellPhase = iota
	shellStarting
	shellRunning
)

type appServerShellOperation struct {
	pending composerDraft
	origin  *string // Submitted turn, until Codex identifies the execution.
	phase   shellPhase
}

func (o *appServerShellOperation) begin(draft composerDraft, turn string) {
	o.pending, o.origin = draft, new(turn)
	if turn == "" {
		o.phase = shellStarting
	}
}
func (o *appServerShellOperation) standalone() bool { return o.phase != shellIdle }
func (o *appServerShellOperation) started(turn string) {
	if o.origin != nil && turn != *o.origin {
		o.phase, o.origin = shellRunning, nil
	} else if o.phase == shellStarting {
		o.phase = shellRunning
	}
}
func (o *appServerShellOperation) acknowledge(failed bool, turn string) composerDraft {
	draft := o.pending
	o.pending = composerDraft{}
	if failed {
		o.identified()
		if turn == "" {
			o.phase = shellIdle
		}
	}
	return draft
}
func (o *appServerShellOperation) identified() { o.origin = nil }
func (o *appServerShellOperation) completed()  { o.phase = shellIdle }
func (o *appServerShellOperation) blocksInput(turn string) bool {
	return o.pending.text != "" || o.standalone() || o.origin != nil && turn != *o.origin
}

type settingsPhase uint8

const (
	settingsIdle settingsPhase = iota
	settingsThread
	settingsTurn
)

type appServerSettingsOperation struct {
	phase         settingsPhase
	change        map[string]any
	turn          string
	restoreEffort bool // Failed restoration remains gated independently of RPCs.
}

func (o *appServerSettingsOperation) pending() bool     { return o.phase != settingsIdle }
func (o *appServerSettingsOperation) blocksInput() bool { return o.pending() || o.restoreEffort }
func (o *appServerSettingsOperation) begin(change map[string]any, turn string) {
	o.phase, o.change, o.turn = settingsThread, change, turn
}
func (o *appServerSettingsOperation) beginLive() { o.phase = settingsTurn }
func (o *appServerSettingsOperation) finish() {
	o.phase, o.change, o.turn = settingsIdle, nil, ""
}
func (o *appServerSettingsOperation) takeChange() map[string]any {
	change := o.change
	o.change = nil
	return change
}
func (o *appServerSettingsOperation) restoredEffort() { o.restoreEffort = false }

type replacementPhase uint8

const (
	replacementIdle replacementPhase = iota
	replacementClear
	replacementResume
	replacementArchive
)

type appServerReplacementOperation struct {
	phase  replacementPhase
	target string
}

func (o *appServerReplacementOperation) pending() bool { return o.phase != replacementIdle }
func (o *appServerReplacementOperation) clear() {
	*o = appServerReplacementOperation{phase: replacementClear}
}
func (o *appServerReplacementOperation) resume(thread string) {
	*o = appServerReplacementOperation{phase: replacementResume, target: thread}
}
func (o *appServerReplacementOperation) finish() { *o = appServerReplacementOperation{} }
