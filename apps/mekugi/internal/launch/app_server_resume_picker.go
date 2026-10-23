package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	json "encoding/json/v2"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/resume_picker.rs:92,989:1000,2033:2088,3469:3490
// @68e1a421. Codex lists saved sessions page by page and filters loaded rows
// locally; it never searches rendered transcript text.
const (
	resumePickerList = "thread/list#picker" // Request label, distinct from --last and restore listings.
	resumePickerPage = 25
)

// appServerResumePicker correlates one host read with its presentation owner.
type appServerResumePicker struct {
	*sessionpresentation.ResumeChooser
	request string
	view    string
}

// /resume follows /clear's rule: the current thread must be idle, so no turn,
// submission or stacked input can be stranded on the thread being left.
func resumeCommand(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, clock func() time.Time, restoring **appServerActivityRestore, resumePicker **appServerResumePicker, environment []string, approvalMode bool, text string) error {
	fields := strings.Fields(text)
	if len(fields) > 2 {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Use /resume or /resume THREAD_ID", true)
		return nil
	}
	if control.Thread == "" || (*restoring) != nil || control.Replacement.Pending() {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be ready", false)
		return nil
	}
	if sessionBusy(&control.Live, attached.conversation) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "/resume is disabled while a task is in progress", true)
		return nil
	}
	prepareDraft(picker, terminal, attached.cwd, environment, (*editor).Take())
	if len(fields) == 2 {
		return resumeSession(attached, terminal, control, picker, clock, resumePicker, approvalMode, fields[1])
	}
	return openResumePicker(attached, control, picker, clock, resumePicker, false)
}

func openResumePicker(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, resumePicker **appServerResumePicker, startup bool) error {
	cwd := attached.cwd
	if startup {
		cwd = control.Startup.Workspace
	}
	picker.cancelScan()
	picker.Open = false
	(*resumePicker) = &appServerResumePicker{ResumeChooser: attached.presentation.OpenResume(startup, cwd), view: attached.view}
	return loadResumePage(control, clock, resumePicker, true)
}

func loadResumePage(control *sessioncontrol.Client[appServerRequest], clock func() time.Time, resumePicker **appServerResumePicker, reset bool) error {
	p := (*resumePicker)
	if reset {
		p.Reset(presentationTime(clock))
	}
	params := sessioncontrol.ResumableThreads(resumePickerPage)
	if !p.All {
		params["cwd"] = p.Workspace
	}
	if p.Cursor != "" {
		params["cursor"] = p.Cursor
	}
	id, err := control.Requests.Send("thread/list", params, viewRequestScope(p.view), appServerRequest{label: resumePickerList, view: p.view})
	if err != nil {
		(*resumePicker).failed("Could not list sessions: " + err.Error())
		return nil
	}
	p.request = id
	p.Sent()
	return nil
}

func resumePickerResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, approvalMode bool, quit *bool, m appserver.Message) error {
	p := (*resumePicker)
	if p == nil || string(m.ID) != p.request {
		return nil
	}
	p.request = ""
	if m.Error != nil {
		(*resumePicker).failed("Could not list sessions: " + m.Error.Message)
		return nil
	}
	var result struct {
		Data []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Preview   string `json:"preview"`
			Cwd       string `json:"cwd"`
			UpdatedAt int64  `json:"updatedAt"`
			GitInfo   *struct {
				Branch string `json:"branch"`
			} `json:"gitInfo"`
		} `json:"data"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		(*resumePicker).failed("Invalid session list: " + err.Error())
		return nil
	}
	var rows []sessionpresentation.ResumeRow
	for _, thread := range result.Data {
		found, err := control.Startup.History.ContainsSession(thread.ID)
		if err != nil {
			p.failed("Could not read session membership: " + err.Error())
			return nil
		}
		if !found {
			continue
		}
		row := sessionpresentation.ResumeRow{ID: thread.ID, Title: strings.TrimSpace(thread.Name), Workspace: thread.Cwd, Updated: thread.UpdatedAt}
		if row.Title == "" {
			row.Title = strings.TrimSpace(thread.Preview)
		}
		if thread.GitInfo != nil {
			row.Branch = thread.GitInfo.Branch
		}
		rows = append(rows, row)
	}
	p.Append(rows, result.NextCursor, len(result.Data))
	if len(rows) == 0 && p.View().CanPage {
		return loadResumePage(control, clock, resumePicker, false)
	}
	if attached.view != "" {
		return nil
	}
	publishPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, (*restoring) != nil, (*side), presentationTime(clock))
	return applyResumeAction(attached, terminal, control, picker, editor, questions, approvals, clock, settings, tiers, title, backend, restoring, side, panel, resumePicker, approvalMode, quit, terminal.Main.Resume.PageDemand())
}

// A failed page stops automatic paging, so the failure stays visible rather
// than being retried every frame. Tab restarts the listing.
func (p *appServerResumePicker) failed(problem string) {
	p.request = ""
	p.Fail(problem)
}

func closeResumePicker(presentation *sessionpresentation.Model, terminal *terminalsession.Attachment) {
	presentation.CloseResume()
	if terminal != nil {
		terminal.Selection = nil
	}
}

// resumeSession switches this UI to a saved thread. The old presentation
// stays usable until Codex confirms the new thread, as for /clear.
func resumeSession(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, resumePicker **appServerResumePicker, approvalMode bool, id string) error {
	if id == control.Thread {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Already viewing this session", false)
		return nil
	}
	if reattachSession(attached, id) {
		return nil
	}
	control.Replacement.Resume(id)
	if err := openSession(attached, control, picker, clock, resumePicker, approvalMode); err != nil {
		control.Replacement.Finish()
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not resume session: "+err.Error(), true)
		return nil
	}
	return nil
}

// A native resume of a live logical thread attaches to its existing execution
// owner. The old view stays usable until the client has received the destination
// snapshot; failed attachment never falls back to a duplicate host resume.
func reattachSession(attached *attachedPresentation, thread string) bool {
	if attached.effects == nil || attached.activeThread == nil || thread == attached.presentation.View().Main.ID || !attached.activeThread(thread) {
		return false
	}
	attached.effects.add(sessionapi.NativeEffect{Thread: attached.presentation.View().Main.ID, Reattach: new(thread)})
	return true
}

// A failed switch keeps the current thread, including events it buffered.
// Input typed for the new session returns to the composer rather than
// reaching the old thread.
func resumeSessionFailed(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, message string) error {
	if attached.reserveThread != nil {
		_ = attached.reserveThread("")
	}
	control.Startup.RewindOriginal = ""
	operation := "resume"
	if control.Replacement.Forking() {
		operation = "fork"
		control.Requests.Retire("fork")
		if target := control.Replacement.Target; target != "" {
			control.RetireThreads(target)
			if err := control.Request("thread/unsubscribe", map[string]any{"threadId": target}); err != nil {
				message += "; releasing fork: " + err.Error()
			}
		}
		control.Startup.ForkSettings, control.Startup.ForkSettingsAck = nil, false
	}
	control.Replacement.Finish()
	control.ResumeEffort = false
	restoreDrafts(attached, terminal, questions, approvals, picker, control.Turn, clock, slices.Concat(attached.conversation.Unsent, attached.conversation.Queued)...)
	attached.conversation.Unsent, attached.conversation.Queued = nil, nil
	attached.presentation.Status.SetText("Ready")
	setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not "+operation+" session: "+message, true)
	return replayResumePending(dispatch, resumePending)
}

func applyResumeAction(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, settings preferences.Applied, tiers *inferencegateway.Tiers, title string, backend string, restoring **appServerActivityRestore, side **sessioncontrol.Side, panel **appServerStatusReport, resumePicker **appServerResumePicker, approvalMode bool, quit *bool, action sessionpresentation.ResumeAction) error {
	if action.Kind == sessionpresentation.ResumeNoAction {
		return nil
	}
	defer func() {
		settlePresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, (*restoring) != nil, (*side), clock, questions, approvals, (*panel) != nil || (*resumePicker) != nil)
	}()
	if err := attached.presentation.AdmitResume(action); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		return nil
	}
	p := (*resumePicker)
	switch action.Kind {
	case sessionpresentation.ResumeSearch:
		p.Query = action.Query
		if attached.view != "" {
			return nil
		}
		publishPresentation(attached, terminal, control, picker, editor, settings, tiers, title, backend, (*restoring) != nil, (*side), presentationTime(clock))
		return applyResumeAction(attached, terminal, control, picker, editor, questions, approvals, clock, settings, tiers, title, backend, restoring, side, panel, resumePicker, approvalMode, quit, terminal.Main.Resume.PageDemand())
	case sessionpresentation.ResumeToggleWorkspace:
		if p.Workspace != "" {
			p.All = !p.All
			return loadResumePage(control, clock, resumePicker, true)
		}
	case sessionpresentation.ResumePage:
		return loadResumePage(control, clock, resumePicker, false)
	case sessionpresentation.ResumeChoose:
		if !p.Startup && sessionBusy(&control.Live, attached.conversation) {
			setNotice(&attached.feedback, terminal, presentationTime(clock), "/resume is disabled while a task is in progress", true)
			return nil
		}
		if reattachSession(attached, action.Thread) {
			return nil
		}
		(*resumePicker) = nil
		closeResumePicker(attached.presentation, terminal)
		if p.Startup {
			control.Startup.Thread = action.Thread
			return openSession(attached, control, picker, clock, resumePicker, approvalMode)
		}
		return resumeSession(attached, terminal, control, picker, clock, resumePicker, approvalMode, action.Thread)
	case sessionpresentation.ResumeDismiss:
		(*resumePicker) = nil
		closeResumePicker(attached.presentation, terminal)
		if p.Startup {
			control.Startup.Thread = ""
			return openSession(attached, control, picker, clock, resumePicker, approvalMode)
		}
	case sessionpresentation.ResumeCancel:
		(*resumePicker) = nil
		closeResumePicker(attached.presentation, terminal)
		if p.Startup {
			(*quit) = true
		}
	}
	return nil
}
