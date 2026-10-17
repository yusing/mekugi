package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	json "encoding/json/v2"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/resume_picker.rs:92,989:1000,2033:2088,3469:3490
// @68e1a421. Codex lists saved sessions page by page and filters loaded rows
// locally; it never searches rendered transcript text.
const (
	resumePickerList = "thread/list#picker" // Request label, distinct from --last and restore listings.
	resumePickerPage = 25
	resumePickerScan = 1000 // Sessions a search loads before it stops fetching.
)

type appServerResumePicker struct {
	id       uint64
	startup  bool   // No session exists yet: Esc starts one, Ctrl-C quits.
	cwd      string // Workspace filter; empty when unavailable.
	all      bool
	query    string
	rows     []sessionpresentation.ResumeRow
	seen     map[string]bool
	cursor   string
	request  string // The in-flight page; any other response is stale.
	loading  bool
	problem  string
	loadedAt time.Time
}

func (p *appServerResumePicker) matches() []sessionpresentation.ResumeRow {
	query := strings.ToLower(strings.TrimSpace(p.query))
	if query == "" {
		return p.rows
	}
	var rows []sessionpresentation.ResumeRow
	for _, row := range p.rows {
		for _, field := range []string{row.Title, row.ID, row.Branch, row.Workspace} {
			if strings.Contains(strings.ToLower(field), query) {
				rows = append(rows, row)
				break
			}
		}
	}
	return rows
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
	u.resumeSequence++
	u.resumePicker = &appServerResumePicker{id: u.resumeSequence, startup: startup, cwd: cwd, all: cwd == ""}
	return u.loadResumePage(true)
}

func (u *appServerUI) loadResumePage(reset bool) error {
	p := u.resumePicker
	if reset {
		p.rows, p.seen, p.cursor = nil, make(map[string]bool), ""
		p.loadedAt = u.now()
	}
	params := sessioncontrol.ResumableThreads(resumePickerPage)
	if !p.all {
		params["cwd"] = p.cwd
	}
	if p.cursor != "" {
		params["cursor"] = p.cursor
	}
	id, err := u.requests.Send("thread/list", params, "main", appServerRequest{label: resumePickerList})
	if err != nil {
		u.resumePickerFailed("Could not list sessions: " + err.Error())
		return nil
	}
	p.request, p.loading, p.problem = id, true, ""
	return nil
}

func (u *appServerUI) resumePickerResponse(m appserver.Message) error {
	p := u.resumePicker
	if p == nil || string(m.ID) != p.request {
		return nil
	}
	p.request, p.loading = "", false
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
	for _, thread := range result.Data {
		if thread.ID == "" || p.seen[thread.ID] {
			continue
		}
		p.seen[thread.ID] = true
		row := sessionpresentation.ResumeRow{ID: thread.ID, Title: strings.TrimSpace(thread.Name), Workspace: thread.Cwd, Updated: thread.UpdatedAt}
		if row.Title == "" {
			row.Title = strings.TrimSpace(thread.Preview)
		}
		if thread.GitInfo != nil {
			row.Branch = thread.GitInfo.Branch
		}
		p.rows = append(p.rows, row)
	}
	p.cursor = result.NextCursor
	u.publishPresentation(u.now())
	return u.applyResumeAction(u.mainView.Resume.PageDemand())
}

// A failed page stops automatic paging, so the failure stays visible rather
// than being retried every frame. Tab restarts the listing.
func (u *appServerUI) resumePickerFailed(problem string) {
	p := u.resumePicker
	p.request, p.loading, p.cursor, p.problem = "", false, "", problem
}

func (u *appServerUI) closeResumePicker() {
	u.resumePicker = nil
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

func (u *appServerUI) resumeView() *sessionpresentation.ResumeView {
	p := u.resumePicker
	if p == nil {
		return nil
	}
	v := &sessionpresentation.ResumeView{ID: p.id, Workspace: p.cwd, Query: p.query, Problem: p.problem,
		Startup: p.startup, All: p.all, Loading: p.loading, HasMore: p.cursor != "", Rows: p.matches(), Scanned: len(p.rows), LoadedAt: p.loadedAt}
	v.CanPage = v.HasMore && !p.loading && (p.query == "" || len(p.rows) < resumePickerScan)
	if len(p.rows) > 0 {
		v.PageAfter = p.rows[len(p.rows)-1].ID
	}
	return v
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
		p.query = action.Query
		u.publishPresentation(u.now())
		return u.applyResumeAction(u.mainView.Resume.PageDemand())
	case sessionpresentation.ResumeToggleWorkspace:
		if p.cwd != "" {
			p.all = !p.all
			return u.loadResumePage(true)
		}
	case sessionpresentation.ResumePage:
		return u.loadResumePage(false)
	case sessionpresentation.ResumeChoose:
		if !p.startup && u.sessionBusy() {
			u.setNotice("/resume is disabled while a task is in progress", true)
			return nil
		}
		u.closeResumePicker()
		if p.startup {
			u.startup.Thread = action.Thread
			return u.openSession()
		}
		return u.resumeSession(action.Thread)
	case sessionpresentation.ResumeDismiss:
		u.closeResumePicker()
		if p.startup {
			u.startup.Thread = ""
			return u.openSession()
		}
	case sessionpresentation.ResumeCancel:
		u.closeResumePicker()
		if p.startup {
			u.quitRequested = true
		}
	}
	return nil
}
