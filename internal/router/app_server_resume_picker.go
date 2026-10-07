package router

import (
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	"github.com/yusing/mekugi/internal/session"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Source: codex-rs/tui/src/resume_picker.rs:92,989:1000,2033:2088,3469:3490
// @68e1a421. Codex lists saved sessions page by page and filters loaded rows
// locally; it never searches rendered transcript text.
const (
	resumePickerStartup = "--pick"             // resumeThread sentinel for a bare `resume`.
	resumePickerList    = "thread/list#picker" // Request label, distinct from --last and restore listings.
	resumePickerPage    = 25
	resumePickerNear    = 5    // Rows below the selection that trigger the next page.
	resumePickerScan    = 1000 // Sessions a search loads before it stops fetching.
)

type appServerResumeRow struct {
	id, title, cwd, branch string
	updated                int64
}

type appServerResumePicker struct {
	startup  bool   // No session exists yet: Esc starts one, Ctrl-C quits.
	cwd      string // Workspace filter; empty when unavailable.
	all      bool
	query    string
	rows     []appServerResumeRow
	seen     map[string]bool
	cursor   string
	request  string // The in-flight page; any other response is stale.
	loading  bool
	problem  string
	selected int // Index into the rows matching query.
	top      int
	viewport int // Row capacity at the last paint.
	loadedAt time.Time
	rowStart int
}

func (p *appServerResumePicker) matches() []appServerResumeRow {
	query := strings.ToLower(strings.TrimSpace(p.query))
	if query == "" {
		return p.rows
	}
	var rows []appServerResumeRow
	for _, row := range p.rows {
		for _, field := range []string{row.title, row.id, row.branch, row.cwd} {
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
	if u.runtime == nil && u.thread == "" || u.restoring != nil || u.replacement.pending() {
		u.setNotice("Wait for the session to be ready", false)
		return nil
	}
	if u.sessionBusy() {
		u.setNotice("/resume is disabled while a task is in progress", true)
		return nil
	}
	if len(fields) == 2 {
		if u.runtime == nil || fields[1] == u.thread {
			u.takeDraft()
		}
		return u.resumeSession(fields[1], "")
	}
	u.takeDraft()
	return u.openResumePicker(false)
}

func (u *appServerUI) openResumePicker(startup bool) error {
	cwd := u.session.cwd
	if startup {
		cwd = u.resumeCwd
	}
	u.cancelPickerScan()
	u.picker.open = false
	u.resumePicker = &appServerResumePicker{startup: startup, cwd: cwd, all: cwd == ""}
	return u.loadResumePage(true)
}

func (u *appServerUI) loadResumePage(reset bool) error {
	p := u.resumePicker
	if reset {
		p.rows, p.seen, p.cursor, p.selected, p.top = nil, make(map[string]bool), "", 0, 0
		p.loadedAt = time.Now()
	}
	if u.runtime != nil {
		client, ok := u.runtime.client.(session.SessionListClient)
		if !ok {
			u.resumePickerFailed("Saved sessions are unavailable for this runtime")
			return nil
		}
		u.runtime.serial++
		id := fmt.Sprintf("sessions/%d", u.runtime.serial)
		cwd := ""
		if !p.all {
			cwd = p.cwd
		}
		if err := client.ListSessions(u.ctx, session.SessionListRequest{ID: id, Cwd: cwd, Cursor: p.cursor, Limit: resumePickerPage}); err != nil {
			u.resumePickerFailed("Could not list sessions: " + err.Error())
			return nil
		}
		p.request, p.loading, p.problem = id, true, ""
		return nil
	}
	params := resumableThreads(resumePickerPage)
	if !p.all {
		params["cwd"] = p.cwd
	}
	if p.cursor != "" {
		params["cursor"] = p.cursor
	}
	id, err := u.requestAs("thread/list", resumePickerList, params)
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
	var rows []appServerResumeRow
	for _, thread := range result.Data {
		row := appServerResumeRow{id: thread.ID, title: strings.TrimSpace(thread.Name), cwd: thread.Cwd, updated: thread.UpdatedAt}
		if row.title == "" {
			row.title = strings.TrimSpace(thread.Preview)
		}
		if thread.GitInfo != nil {
			row.branch = thread.GitInfo.Branch
		}
		rows = append(rows, row)
	}
	return u.receiveResumePage(rows, result.NextCursor)
}

func (u *appServerUI) receiveResumePage(rows []appServerResumeRow, cursor string) error {
	p := u.resumePicker
	for _, row := range rows {
		if row.id == "" || p.seen[row.id] {
			continue
		}
		p.seen[row.id] = true
		p.rows = append(p.rows, row)
	}
	p.cursor = cursor
	return u.fillResumePicker()
}

// A failed page stops automatic paging, so the failure stays visible rather
// than being retried every frame. Tab restarts the listing.
func (u *appServerUI) resumePickerFailed(problem string) {
	p := u.resumePicker
	p.request, p.loading, p.cursor, p.problem = "", false, "", problem
}

// fillResumePicker loads another page while the selection nears the end of
// the matching rows, including a search that has not matched yet, or while
// the painted viewport is not yet full.
func (u *appServerUI) fillResumePicker() error {
	p := u.resumePicker
	if p == nil || p.loading || p.cursor == "" {
		return nil
	}
	if p.query != "" && len(p.rows) >= resumePickerScan {
		return nil
	}
	if len(p.matches()) > max(p.selected+resumePickerNear, p.viewport) {
		return nil
	}
	return u.loadResumePage(false)
}

func (u *appServerUI) closeResumePicker() {
	u.resumePicker = nil
	if u.shell != nil {
		u.shell.selection = nil
	}
}

// resumeSession switches this UI to a saved thread. The old presentation
// stays usable until Codex confirms the new thread, as for /clear.
func (u *appServerUI) resumeSession(id, workspace string) error {
	if id == u.thread {
		u.setNotice("Already viewing this session", false)
		return nil
	}
	if u.runtime != nil {
		return u.changeRuntimeSession(id, workspace)
	}
	u.replacement.resume(id)
	if err := u.requestResume(id); err != nil {
		u.replacement.finish()
		return err
	}
	return nil
}

// A failed switch keeps the current thread, including events it buffered.
// Input typed for the new session returns to the composer rather than
// reaching the old thread.
func (u *appServerUI) resumeSessionFailed(message string) error {
	u.replacement.finish()
	u.resumePendingEffort = false
	u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
	u.unsent, u.queued = nil, nil
	u.status = "Ready"
	u.setNotice("Could not resume session: "+message, true)
	return u.replayResumePending()
}

func (u *appServerUI) startThread() error {
	u.status = "Starting thread…"
	return u.request("thread/start", map[string]any{"approvalPolicy": "never", "sandbox": "danger-full-access"})
}

// resumePickerKey handles every key while the picker is open.
func (u *appServerUI) resumePickerKey(key string) error {
	p := u.resumePicker
	rows := p.matches()
	move := func(to int) {
		p.selected = max(0, min(len(rows)-1, to))
	}
	switch key {
	case "\x1b[200~", "\x1b[201~":
	case "\x1b":
		if p.query != "" {
			p.query, p.selected, p.top = "", 0, 0
			break
		}
		u.closeResumePicker()
		if p.startup {
			u.resumeThread = ""
			return u.startThread()
		}
		return nil
	case "\x03":
		u.closeResumePicker()
		if p.startup {
			u.quitRequested = true
		}
		return nil
	case "\x1b[A", "\x1bOA", "\x10":
		move(p.selected - 1)
	case "\x1b[B", "\x1bOB", "\x0e":
		move(p.selected + 1)
	case "\x1b[5~":
		move(p.selected - max(1, p.viewport))
	case "\x1b[6~":
		move(p.selected + max(1, p.viewport))
	case "\x1b[H", "\x1bOH":
		move(0)
	case "\x1b[F", "\x1bOF":
		move(len(rows) - 1)
	case "\t":
		if p.cwd != "" {
			p.all = !p.all
			return u.loadResumePage(true)
		}
	case "\r":
		if len(rows) == 0 {
			break
		}
		row := rows[p.selected]
		id := row.id
		u.closeResumePicker()
		if p.startup {
			u.resumeThread = id
			return u.requestResume(id)
		}
		return u.resumeSession(id, row.cwd)
	case "\x7f", "\b":
		if p.query != "" {
			_, n := utf8.DecodeLastRuneInString(p.query)
			p.query, p.selected, p.top = p.query[:len(p.query)-n], 0, 0
		}
	default:
		// Other control and escape sequences are not search text.
		if key == "" || key[0] < 32 || key[0] == 0x7f {
			return nil
		}
		p.query += key
		p.selected, p.top = 0, 0
	}
	return u.fillResumePicker()
}

func resumePickerAge(now time.Time, updated int64) string {
	if updated == 0 {
		return "-"
	}
	d := now.Sub(time.Unix(updated, 0))
	switch {
	case d < time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// The picker replaces Main, as the stock resume screen replaces the chat.
// Column widths come from the visible rows only.
func (u *appServerUI) resumePickerFrame(width, height int) []string {
	p := u.resumePicker
	u.mainContentPainted = false
	u.composerRect = terminalRect{}
	frame := make([]string, height)
	put := func(y int, text string) {
		if y >= 0 && y < height {
			frame[y] = ansi.Truncate(text, width, "…")
		}
	}
	bold, dim, reset := "\x1b[1m", activityui.Dim, activityui.Reset
	accent := "\x1b[38;2;104;174;245m"
	if u.view.painter.Theme == livediff.LightTheme {
		accent = "\x1b[38;2;25;94;164m"
	}
	option := func(label string, on bool) string {
		if on {
			return bold + accent + label + reset
		}
		return dim + label + reset
	}
	put(0, "  "+bold+"Resume a previous session"+reset)
	filter := "  " + dim + "Filter:" + reset + " "
	if p.cwd != "" {
		filter += option("Cwd", !p.all) + " " + option("All", p.all)
		if !p.all {
			filter += dim + " · " + reset + pickerText(p.cwd)
		}
	} else {
		filter += option("All", true)
	}
	put(1, filter)
	search := "  " + dim + "Type to search" + reset
	if p.query != "" {
		search = "  Search: " + pickerText(p.query)
	}
	if p.problem != "" {
		search = "  \x1b[38;2;239;117;117m" + pickerText(p.problem) + reset
	}
	put(2, search)
	footer := "  " + bold + "enter" + reset + dim + " resume · " + reset + bold + "esc" + reset + dim
	switch {
	case p.query != "":
		footer += " clear search"
	case p.startup:
		footer += " start new"
	default:
		footer += " close"
	}
	if p.cwd != "" {
		footer += " · " + reset + bold + "tab" + reset + dim + " cwd/all"
	}
	footer += " · " + reset + bold + "ctrl+c" + reset + dim
	if p.startup {
		footer += " quit"
	} else {
		footer += " close"
	}
	footer += " · " + reset + bold + "↑/↓" + reset + dim + " browse" + reset
	put(height-1, footer)

	rows := p.matches()
	first, last := 5, height-2 // Header row at 4; the row before the footer marks more.
	if height < 8 {
		first, last = min(3, height-1), height-1
	}
	p.viewport = max(1, last-first)
	p.selected = max(0, min(p.selected, len(rows)-1))
	p.top = max(0, min(p.top, p.selected, len(rows)-p.viewport))
	if p.selected >= p.top+p.viewport {
		p.top = p.selected - p.viewport + 1
	}
	p.rowStart = first
	if len(rows) == 0 {
		message := "No sessions yet"
		switch {
		case p.loading && p.query != "":
			message = "Searching…"
		case p.loading:
			message = "Loading sessions…"
		case p.query != "" && p.cursor != "":
			message = fmt.Sprintf("Search scanned first %d sessions; more may exist", len(p.rows))
		case p.query != "":
			message = "No results for your search"
		}
		put(first, "  "+dim+message+reset)
		return frame
	}
	visible := rows[p.top:min(len(rows), p.top+p.viewport)]
	ageWidth, branchWidth, cwdWidth := len("Updated"), len("Branch"), len("Directory")
	for _, row := range visible {
		ageWidth = max(ageWidth, ansi.StringWidth(resumePickerAge(p.loadedAt, row.updated)))
		branchWidth = max(branchWidth, ansi.StringWidth(pickerText(row.branch)))
		cwdWidth = max(cwdWidth, ansi.StringWidth(pickerText(pathdisplay.ForWorkspace(p.cwd, row.cwd))))
	}
	branchWidth, cwdWidth = min(branchWidth, 24), min(cwdWidth, max(12, width/3))
	showBranch := width >= 60
	showCwd := p.all && width >= 90
	pad := func(text string, n int) string {
		text = ansi.Truncate(text, n, "…")
		return text + strings.Repeat(" ", max(0, n-ansi.StringWidth(text)))
	}
	columns := func(age, branch, cwd, title string) string {
		text := pad(age, ageWidth) + "  "
		if showBranch {
			text += pad(branch, branchWidth) + "  "
		}
		if showCwd {
			text += pad(cwd, cwdWidth) + "  "
		}
		return text + title
	}
	if first > 3 {
		header := columns("Updated", "Branch", "Directory", "Conversation")
		if p.top > 0 {
			header += "  ↑ more"
		}
		put(first-1, "  "+dim+header+reset)
	}
	for i, row := range visible {
		index := p.top + i
		title := pickerText(row.title)
		if title == "" {
			title = "(no message yet)"
		}
		if row.id == u.thread {
			title += dim + " · current" + reset
		}
		prefix := "  "
		if index == p.selected {
			prefix = "› "
		}
		text := prefix + columns(resumePickerAge(p.loadedAt, row.updated), pickerText(row.branch), pickerText(pathdisplay.ForWorkspace(p.cwd, row.cwd)), title)
		text = ansi.Truncate(text, width, "…")
		if index == p.selected {
			text = u.pickerSelection() + ansi.Strip(text) + strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + reset
		}
		put(first+i, text)
	}
	switch {
	case last == height-1: // The footer keeps its row in a short pane.
	case p.loading:
		put(last, "  "+dim+"↓ loading more"+reset)
	case p.top+p.viewport < len(rows) || p.cursor != "" && p.query == "":
		put(last, "  "+dim+"↓ more"+reset)
	}
	return frame
}

// resumePickerMouse scrolls with the wheel and selects a clicked row.
func (u *appServerUI) resumePickerMouse(button, y int) error {
	p := u.resumePicker
	switch button {
	case 64:
		return u.resumePickerKey("\x1b[A")
	case 65:
		return u.resumePickerKey("\x1b[B")
	case 0:
		if row := p.top + y - p.rowStart; y >= p.rowStart && y-p.rowStart < p.viewport && row < len(p.matches()) {
			p.selected = row
		}
	}
	return nil
}
