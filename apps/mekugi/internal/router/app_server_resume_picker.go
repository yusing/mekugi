package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

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
}

// /resume follows /clear's rule: the current thread must be idle, so no turn,
// submission or stacked input can be stranded on the thread being left.
func (u *appServerUI) resumeCommand(text string) error {
	fields := strings.Fields(text)
	if len(fields) > 2 {
		u.setNotice("Use /resume or /resume THREAD_ID", true)
		return nil
	}
	if u.Thread == "" || u.restoring != nil || u.Replacement.Pending() {
		u.setNotice("Wait for the session to be ready", false)
		return nil
	}
	if u.sessionBusy() {
		u.setNotice("/resume is disabled while a task is in progress", true)
		return nil
	}
	u.takeDraft()
	if len(fields) == 2 {
		return u.resumeSession(fields[1])
	}
	return u.openResumePicker(false)
}

func (u *appServerUI) openResumePicker(startup bool) error {
	cwd := u.cwd
	if startup {
		cwd = u.startup.Workspace
	}
	u.cancelPickerScan()
	u.picker.Open = false
	u.resumePicker = &appServerResumePicker{ResumeChooser: u.sessionPresentation().OpenResume(startup, cwd)}
	return u.loadResumePage(true)
}

func (u *appServerUI) loadResumePage(reset bool) error {
	p := u.resumePicker
	if reset {
		p.Reset(u.now())
	}
	params := sessioncontrol.ResumableThreads(resumePickerPage)
	if !p.All {
		params["cwd"] = p.Workspace
	}
	if p.Cursor != "" {
		params["cursor"] = p.Cursor
	}
	id, err := u.requests.Send("thread/list", params, "main", appServerRequest{label: resumePickerList})
	if err != nil {
		u.resumePickerFailed("Could not list sessions: " + err.Error())
		return nil
	}
	p.request = id
	p.Sent()
	return nil
}

func (u *appServerUI) resumePickerResponse(m appserver.Message) error {
	p := u.resumePicker
	if p == nil || string(m.ID) != p.request {
		return nil
	}
	p.request = ""
	if m.Error != nil {
		u.resumePickerFailed("Could not list sessions: " + m.Error.Message)
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
		u.resumePickerFailed("Invalid session list: " + err.Error())
		return nil
	}
	var rows []sessionpresentation.ResumeRow
	for _, thread := range result.Data {
		row := sessionpresentation.ResumeRow{ID: thread.ID, Title: strings.TrimSpace(thread.Name), Workspace: thread.Cwd, Updated: thread.UpdatedAt}
		if row.Title == "" {
			row.Title = strings.TrimSpace(thread.Preview)
		}
		if thread.GitInfo != nil {
			row.Branch = thread.GitInfo.Branch
		}
		rows = append(rows, row)
	}
	p.Append(rows, result.NextCursor)
	u.publishPresentation(u.now())
	return u.applyResumeAction(u.mainView.Resume.PageDemand())
}

// A failed page stops automatic paging, so the failure stays visible rather
// than being retried every frame. Tab restarts the listing.
func (u *appServerUI) resumePickerFailed(problem string) {
	p := u.resumePicker
	p.request = ""
	p.Fail(problem)
}

func (u *appServerUI) closeResumePicker() {
	u.resumePicker = nil
	u.sessionPresentation().CloseResume()
	if u.shell != nil {
		u.shell.Selection = nil
	}
}

// resumeSession switches this UI to a saved thread. The old presentation
// stays usable until Codex confirms the new thread, as for /clear.
func (u *appServerUI) resumeSession(id string) error {
	if id == u.Thread {
		u.setNotice("Already viewing this session", false)
		return nil
	}
	u.Replacement.Resume(id)
	if err := u.openSession(); err != nil {
		u.Replacement.Finish()
		return err
	}
	return nil
}

// A failed switch keeps the current thread, including events it buffered.
// Input typed for the new session returns to the composer rather than
// reaching the old thread.
func (u *appServerUI) resumeSessionFailed(message string) error {
	u.Replacement.Finish()
	u.ResumeEffort = false
	u.restoreDrafts(slices.Concat(u.conversation().Unsent, u.conversation().Queued)...)
	u.conversation().Unsent, u.conversation().Queued = nil, nil
	u.sessionPresentation().Status.SetText("Ready")
	u.setNotice("Could not resume session: "+message, true)
	return u.replayResumePending()
}

func (u *appServerUI) applyResumeAction(action sessionpresentation.ResumeAction) error {
	if action.Kind == sessionpresentation.ResumeNoAction {
		return nil
	}
	defer u.settlePresentation()
	if err := u.sessionPresentation().AdmitResume(action); err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	p := u.resumePicker
	switch action.Kind {
	case sessionpresentation.ResumeSearch:
		p.Query = action.Query
		u.publishPresentation(u.now())
		return u.applyResumeAction(u.mainView.Resume.PageDemand())
	case sessionpresentation.ResumeToggleWorkspace:
		if p.Workspace != "" {
			p.All = !p.All
			return u.loadResumePage(true)
		}
	case sessionpresentation.ResumePage:
		return u.loadResumePage(false)
	case sessionpresentation.ResumeChoose:
		if !p.Startup && u.sessionBusy() {
			u.setNotice("/resume is disabled while a task is in progress", true)
			return nil
		}
		u.closeResumePicker()
		if p.Startup {
			u.startup.Thread = action.Thread
			return u.openSession()
		}
		return u.resumeSession(action.Thread)
	case sessionpresentation.ResumeDismiss:
		u.closeResumePicker()
		if p.Startup {
			u.startup.Thread = ""
			return u.openSession()
		}
	case sessionpresentation.ResumeCancel:
		u.closeResumePicker()
		if p.Startup {
			u.quitRequested = true
		}
	}
	return nil
}
