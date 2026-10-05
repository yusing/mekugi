package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"mvdan.cc/sh/v3/shell"
)

// nativeApprovalChoice is one answer to a host request.
type nativeApprovalChoice struct {
	label    string
	response map[string]any
	outcome  string // Transcript wording once chosen.
}

// nativeApproval is a pending Codex approval server request. Its options follow the stock
// TUI's approval overlay.
// Source: codex-rs/tui/src/bottom_pane/approval_overlay.rs exec_options,
// patch_options and permissions_options @7135b303d.
type nativeApproval struct {
	thread, turn string
	request      jsontext.Value // Host server request.
	title        string
	subject      string   // Command or summary, for the dock and the transcript.
	details      []string // Directory, reason and requested scope.
	choices      []nativeApprovalChoice
	selected     int
	textTop      int
}

// nativeApprovalDock shows the oldest pending approval above the composer.
// Like the question dock, it opens by itself only over an empty, idle
// composer, so typing cannot answer a request that appeared mid-keystroke.
type nativeApprovalDock struct {
	pending  []*nativeApproval
	open     bool
	autoOpen bool
	painted  bool
	patches  map[string][]string // Live fileChange item paths, for edit approvals.
}

// threadPermissions keeps --yolo's policy on thread requests. Approval mode
// leaves both settings to Codex configuration and invocation overrides.
func (u *appServerUI) threadPermissions(params map[string]any) map[string]any {
	if !u.approvalMode {
		params["approvalPolicy"], params["sandbox"] = "never", "danger-full-access"
	}
	return params
}

func (u *appServerUI) approvalAgent(thread string) string {
	path := u.session.paths[thread]
	if path == "" || path == "/root" {
		return ""
	}
	return path
}

// approvalDirectory names a request's directory unless it is the workspace.
func (u *appServerUI) approvalDirectory(cwd string) []string {
	if cwd == "" {
		return nil
	}
	if path := pathdisplay.ForWorkspace(u.session.cwd, cwd); path != "." {
		return []string{"in " + path}
	}
	return nil
}

func (u *appServerUI) addApproval(a *nativeApproval) {
	u.approvals.pending = append(u.approvals.pending, a)
	u.approvals.autoOpen = true
	if a.request == nil {
		u.notify("approval-requested", "Approval requested")
	}
	u.autoOpenApprovals()
}

// approvalMessage answers Codex's approval requests. Resolution and turn
// completion notifications also reach their other owners.
func (u *appServerUI) approvalMessage(m appserver.Message) (bool, error) {
	switch m.Method {
	case "serverRequest/resolved":
		var p struct {
			ThreadID  string         `json:"threadId"`
			RequestID jsontext.Value `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			for _, a := range slices.Clone(u.approvals.pending) {
				if a.request != nil && string(a.request) == string(p.RequestID) && (p.ThreadID == "" || a.thread == p.ThreadID) {
					u.endApproval(a, "Resolved elsewhere")
				}
			}
		}
		return false, nil
	case "turn/completed":
		var p struct {
			ThreadID string        `json:"threadId"`
			Turn     appServerTurn `json:"turn"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			for _, a := range slices.Clone(u.approvals.pending) {
				if a.request != nil && a.thread == p.ThreadID && a.turn == p.Turn.ID {
					u.endApproval(a, "Turn ended before an answer")
				}
			}
		}
		return false, nil
	case "item/started", "item/completed":
		var p struct {
			Item appServerItem `json:"item"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.Item.Type == "fileChange" {
			if u.approvals.patches == nil {
				u.approvals.patches = make(map[string][]string)
			}
			if m.Method == "item/started" {
				var paths []string
				for _, change := range p.Item.Changes {
					paths = append(paths, pathdisplay.ForWorkspace(u.session.cwd, change.Path))
				}
				u.approvals.patches[p.Item.ID] = paths
			} else {
				delete(u.approvals.patches, p.Item.ID)
			}
		}
		return false, nil
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval":
	default:
		return false, nil
	}
	if len(m.ID) == 0 {
		return false, nil
	}
	var p struct {
		Kind           string `json:"kind"`
		ThreadID       string `json:"threadId"`
		TurnID         string `json:"turnId"`
		ItemID         string `json:"itemId"`
		Reason         string `json:"reason"`
		Command        string `json:"command"`
		Cwd            string `json:"cwd"`
		GrantRoot      string `json:"grantRoot"`
		NetworkContext *struct {
			Host string `json:"host"`
		} `json:"networkApprovalContext"`
		Additional         jsontext.Value   `json:"additionalPermissions"`
		Amendment          []string         `json:"proposedExecpolicyAmendment"`
		Permissions        jsontext.Value   `json:"permissions"`
		AvailableDecisions []jsontext.Value `json:"availableDecisions"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return true, fmt.Errorf("%s: %w", m.Method, err)
	}
	a := &nativeApproval{thread: p.ThreadID, turn: p.TurnID, request: slices.Clone(m.ID), details: u.approvalDirectory(p.Cwd)}
	switch m.Method {
	case "item/commandExecution/requestApproval":
		a.title, a.subject = "Run this command?", appServerDisplayCommand(p.Command)
		if p.NetworkContext != nil {
			a.title = "Allow network access to " + p.NetworkContext.Host + "?"
		}
		if p.Kind == "writeStdin" {
			// Codex shell-joins write_stdin --session-id ID INPUT.
			// Source: codex-rs/core/src/tools/approvals.rs ApprovalAction::WriteStdin @7135b303d.
			a.title = "Send input to the running terminal?"
			if words, err := shell.Fields(p.Command, func(string) string { return "" }); err == nil && len(words) == 4 && words[0] == "write_stdin" {
				a.title, a.subject = "Send input to terminal "+words[2]+"?", "Input: "+strconv.Quote(words[3])
			}
		}
		if summary := permissionSummary(p.Additional); summary != "" {
			a.details = append(a.details, "Permissions: "+summary)
		}
		decisions := p.AvailableDecisions
		if len(decisions) == 0 {
			// Codex's default when the request names no decisions.
			decisions = []jsontext.Value{jsontext.Value(`"accept"`)}
			if len(p.Amendment) > 0 {
				amendment, _ := json.Marshal(map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": p.Amendment}})
				decisions = append(decisions, amendment)
			}
			decisions = append(decisions, jsontext.Value(`"cancel"`))
		}
		for _, decision := range decisions {
			if choice, ok := commandApprovalChoice(decision, p.NetworkContext != nil, len(p.Additional) > 0 && string(p.Additional) != "null"); ok {
				a.choices = append(a.choices, choice)
			}
		}
	case "item/fileChange/requestApproval":
		a.title = "Make these edits?"
		paths := u.approvals.patches[p.ItemID]
		a.subject = strings.Join(paths, "\n")
		if a.subject == "" {
			a.subject = "Proposed edits"
		}
		if p.GrantRoot != "" {
			a.details = append(a.details, "Allow writes under "+pathdisplay.ForWorkspace(u.session.cwd, p.GrantRoot)+" for this session")
		}
		for _, choice := range []struct{ label, decision, outcome string }{
			{"Yes, proceed", "accept", "Approved edits"},
			{"Yes, and don't ask again for these files", "acceptForSession", "Approved edits for this session"},
			{"No, and tell Codex what to do differently", "cancel", "Declined edits"},
		} {
			a.choices = append(a.choices, nativeApprovalChoice{label: choice.label, response: map[string]any{"decision": choice.decision}, outcome: choice.outcome})
		}
	case "item/permissions/requestApproval":
		a.title, a.subject = "Grant these permissions?", permissionSummary(p.Permissions)
		if a.subject == "" {
			a.subject = "Additional permissions"
		}
		granted := p.Permissions
		if len(granted) == 0 {
			granted = jsontext.Value(`{}`)
		}
		a.choices = []nativeApprovalChoice{
			{label: "Yes, grant these permissions for this turn", response: map[string]any{"permissions": granted, "scope": "turn"}, outcome: "Granted permissions for this turn"},
			{label: "Yes, grant for this turn with strict auto review", response: map[string]any{"permissions": granted, "scope": "turn", "strictAutoReview": true}, outcome: "Granted permissions with strict auto review"},
			{label: "Yes, grant these permissions for this session", response: map[string]any{"permissions": granted, "scope": "session"}, outcome: "Granted permissions for this session"},
			{label: "No, continue without permissions", response: map[string]any{"permissions": map[string]any{}, "scope": "turn"}, outcome: "Denied permissions"},
		}
	}
	if p.Reason != "" {
		a.details = append(a.details, "Reason: "+p.Reason)
	}
	if len(a.choices) == 0 {
		return false, nil // Leave an unanswerable request visibly blocked.
	}
	u.blockNotification(m)
	u.addApproval(a)
	return true, nil
}

// commandApprovalChoice labels one of the host's offered decisions, which is
// returned unchanged.
func commandApprovalChoice(decision jsontext.Value, network, permissions bool) (nativeApprovalChoice, bool) {
	choice := nativeApprovalChoice{response: map[string]any{"decision": decision}}
	var name string
	if json.Unmarshal(decision, &name) == nil {
		switch name {
		case "accept":
			choice.label, choice.outcome = "Yes, proceed", "Approved"
			if network {
				choice.label = "Yes, just this once"
			}
		case "acceptForSession":
			choice.label, choice.outcome = "Yes, and don't ask again for this command in this session", "Approved for this session"
			if network {
				choice.label = "Yes, and allow this host for this conversation"
			} else if permissions {
				choice.label = "Yes, and allow these permissions for this session"
			}
		case "decline":
			choice.label, choice.outcome = "No, continue without running it", "Declined"
		case "cancel":
			choice.label, choice.outcome = "No, and tell Codex what to do differently", "Declined"
		default:
			return choice, false
		}
		return choice, true
	}
	var variant map[string]struct {
		Command []string `json:"execpolicy_amendment"`
		Network *struct {
			Host   string `json:"host"`
			Action string `json:"action"`
		} `json:"network_policy_amendment"`
	}
	if json.Unmarshal(decision, &variant) != nil || len(variant) != 1 {
		return choice, false
	}
	for name, value := range variant {
		switch {
		case name == "acceptWithExecpolicyAmendment" && len(value.Command) > 0:
			prefix := workerCommand(value.Command[0], value.Command[1:])
			if strings.ContainsAny(prefix, "\r\n") {
				return choice, false
			}
			choice.label = "Yes, and don't ask again for commands that start with `" + prefix + "`"
			choice.outcome = "Approved commands starting with " + prefix
		case name == "applyNetworkPolicyAmendment" && value.Network != nil:
			if value.Network.Action == "deny" {
				choice.label, choice.outcome = "No, and block this host in the future", "Blocked "+value.Network.Host
			} else {
				choice.label, choice.outcome = "Yes, and allow this host in the future", "Allowed "+value.Network.Host
			}
		default:
			return choice, false
		}
	}
	return choice, true
}

// permissionSummary condenses a requested permission profile, following the
// stock overlay's rule text.
// Source: codex-rs/tui/src/bottom_pane/approval_overlay.rs
// format_additional_permissions_rule @7135b303d.
func permissionSummary(value jsontext.Value) string {
	var profile struct {
		Network *struct {
			Enabled *bool `json:"enabled"`
		} `json:"network"`
		FileSystem *struct {
			Read    []string `json:"read"`
			Write   []string `json:"write"`
			Entries []struct {
				Access string         `json:"access"`
				Path   jsontext.Value `json:"path"`
			} `json:"entries"`
		} `json:"fileSystem"`
	}
	if len(value) == 0 || json.Unmarshal(value, &profile) != nil {
		return ""
	}
	var parts []string
	if profile.Network != nil && profile.Network.Enabled != nil && *profile.Network.Enabled {
		parts = append(parts, "network")
	}
	if fs := profile.FileSystem; fs != nil {
		access := map[string][]string{"read": fs.Read, "write": fs.Write}
		for _, entry := range fs.Entries {
			var path struct {
				Path    string `json:"path"`
				Pattern string `json:"pattern"`
				Value   any    `json:"value"`
			}
			label := ""
			if json.Unmarshal(entry.Path, &label) != nil && json.Unmarshal(entry.Path, &path) == nil {
				switch {
				case path.Pattern != "":
					label = "glob " + path.Pattern
				case path.Path != "":
					label = path.Path
				case path.Value != nil:
					label = fmt.Sprint(path.Value)
				}
			}
			if label != "" {
				access[entry.Access] = append(access[entry.Access], label)
			}
		}
		for _, mode := range []string{"read", "write", "deny"} {
			if len(access[mode]) > 0 {
				label := mode
				if mode == "deny" {
					label = "deny read"
				}
				parts = append(parts, label+" "+strings.Join(access[mode], ", "))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// endApproval removes a pending approval and records how it ended.
func (u *appServerUI) endApproval(a *nativeApproval, outcome string) {
	index := slices.Index(u.approvals.pending, a)
	if index < 0 {
		return
	}
	u.approvals.pending = slices.Delete(u.approvals.pending, index, index+1)
	if len(u.approvals.pending) == 0 {
		u.approvals.open = false
	}
	if a.request != nil && u.notifications != nil {
		delete(u.notifications.blocked, string(a.request))
	}
	text := outcome + " · " + a.subject
	if agent := u.approvalAgent(a.thread); agent != "" {
		text += " · " + agent
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: text, Observed: u.now()}}})
}

func (u *appServerUI) answerApproval(a *nativeApproval, choice nativeApprovalChoice) error {
	if err := u.client.Respond(a.request, choice.response); err != nil {
		return err
	}
	u.endApproval(a, choice.outcome)
	return nil
}

// autoOpenApprovals takes over an empty, idle composer. Explicitly hiding the
// dock leaves it closed until reopened or another approval arrives.
func (u *appServerUI) autoOpenApprovals() {
	if u.approvals.autoOpen && !u.approvals.open && len(u.approvals.pending) > 0 && u.questions.active == nil && u.composerVacant() {
		u.openApprovals()
	}
}

func (u *appServerUI) openApprovals() bool {
	if len(u.approvals.pending) == 0 {
		return false
	}
	u.hideQuestions()
	u.approvals.open, u.approvals.painted = true, false
	u.picker.open, u.keybindings = false, false
	u.cancelPickerScan()
	if u.shell != nil {
		u.shell.focus = 0
		u.shell.selection = nil
	}
	return true
}

func (u *appServerUI) approvalKey(key string) (bool, error) {
	if !u.approvals.open || len(u.approvals.pending) == 0 {
		return false, nil
	}
	a := u.approvals.pending[0]
	switch key {
	case "\x1b":
		u.approvals.open, u.approvals.autoOpen = false, false
	case "\x1b[A", "\x10":
		a.selected = (a.selected - 1 + len(a.choices)) % len(a.choices)
	case "\x1b[B", "\x0e":
		a.selected = (a.selected + 1) % len(a.choices)
	case "\x1b[5~":
		a.textTop = max(0, a.textTop-1)
	case "\x1b[6~":
		a.textTop++
	case "\r":
		return true, u.answerApproval(a, a.choices[a.selected])
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			if index := int(key[0] - '1'); index < len(a.choices) {
				a.selected = index
			}
			return true, nil
		}
		// No key may edit the hidden draft or answer by accident. Ctrl-C keeps
		// its meaning, Ctrl-B its pane commands, and other sequences, such as
		// mouse reports, theirs.
		if len(key) == 1 {
			return key != "\x03" && key != "\x02", nil
		}
		return key == "\x1b[3~", nil
	}
	return true, nil
}

func (u *appServerUI) approvalRows(width, height int) []string {
	d := &u.approvals
	if len(d.pending) == 0 {
		return nil
	}
	p := &u.view.painter
	accent, dim, reset := p.Theme.Accent(), activityui.Dim, activityui.Reset
	if !d.open {
		label := accent + "! " + reset + fmt.Sprintf("%d approval", len(d.pending))
		if len(d.pending) > 1 {
			label += "s"
		}
		label += " pending" + dim + " · " + reset + accent + "ctrl+b q" + reset + dim + " review" + reset
		return []string{ansi.Truncate(label, width, "…")}
	}
	a := d.pending[0]
	padding := min(2, max(0, (width-20)/2))
	inner := max(1, width-2*padding)
	head := "! " + a.title
	state := u.approvalAgent(a.thread)
	if len(d.pending) > 1 {
		state = strings.TrimPrefix(state+fmt.Sprintf(" · 1 of %d", len(d.pending)), " · ")
	}
	header := accent + "\x1b[1m" + head + reset
	if gap := inner - ansi.StringWidth(head) - ansi.StringWidth(state); state != "" && gap >= 2 {
		header += strings.Repeat(" ", gap) + dim + state + reset
	}
	rows := []string{ansi.Truncate(header, inner, "")}
	foot := pickerWrap(accent+fmt.Sprintf("1–%d", len(a.choices))+reset+dim+" choose · "+reset+accent+"enter"+reset+dim+" confirm · "+reset+accent+"esc"+reset+dim+" hide"+reset, inner)
	var text []string
	for _, line := range strings.Split(a.subject, "\n") {
		text = append(text, pickerWrap(livediff.Safe(line, false), inner)...)
	}
	for _, detail := range a.details {
		for _, line := range pickerWrap(livediff.Safe(detail, false), inner) {
			text = append(text, dim+line+reset)
		}
	}
	// The request text takes precedence over spacing; it scrolls only when
	// even the unspaced dock cannot hold it.
	fixed := len(rows) + len(a.choices) + len(foot)
	spaced := fixed+len(text)+3 <= height
	textRoom := height - fixed
	if spaced {
		textRoom -= 3
		rows = append(rows, "")
	}
	if len(text) > textRoom {
		textRoom--
	}
	textRoom = max(1, textRoom)
	a.textTop = min(a.textTop, max(0, len(text)-textRoom))
	rows = append(rows, text[a.textTop:min(len(text), a.textTop+textRoom)]...)
	if len(text) > textRoom {
		rows = append(rows, dim+ansi.Truncate("pgup/pgdn request text", inner, "")+reset)
	}
	if spaced {
		rows = append(rows, "")
	}
	for i, choice := range a.choices {
		prefix := "  "
		if i == a.selected {
			prefix = "› "
		}
		line := ansi.Truncate(fmt.Sprintf("%s%d. %s", prefix, i+1, pickerText(choice.label)), inner, "…")
		if i == a.selected {
			line = u.pickerSelection() + ansi.Strip(line) + strings.Repeat(" ", max(0, inner-ansi.StringWidth(line))) + reset
		}
		rows = append(rows, line)
	}
	if spaced {
		rows = append(rows, "")
	}
	rows = append(rows, foot...)
	for i := range rows {
		rows[i] = strings.Repeat(" ", padding) + rows[i]
	}
	return rows[:min(len(rows), height)]
}
