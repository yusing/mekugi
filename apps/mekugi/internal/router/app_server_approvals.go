package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/contracts/approval"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

// Live response payloads and command-guard handles stay in this adapter.
type nativeApproval struct {
	*prompt.Approval
	request   jsontext.Value
	responses []map[string]any
	guard     *vcsApproval
	layout    conversationui.ApprovalLayout
}
type nativeApprovalDock struct {
	*prompt.Approvals
	handles map[*prompt.Approval]*nativeApproval
	unbound []*nativeApproval
	painted bool
	patches map[string][]string
	grants  map[binding.ShellGuardKind]binding.CommandGrants
}

func (u *appServerUI) pendingApprovals() []*nativeApproval {
	result := make([]*nativeApproval, 0, len(u.approvals.Pending))
	for _, a := range u.approvals.Pending {
		result = append(result, u.approvals.handles[a])
	}
	return result
}

func (d *nativeApprovalDock) initGuards(guards []binding.ShellApproval) {
	d.grants = make(map[binding.ShellGuardKind]binding.CommandGrants, len(guards))
	for _, guard := range guards {
		d.grants[guard.Kind] = guard.NewSession()
	}
}

func guardApprovalIdentity(request *vcsApproval) approval.Command {
	command, _ := json.Marshal(request.argv)
	return approval.Command{Directory: request.cwd, Executable: request.executable, Arguments: string(command)}
}

// threadPermissions keeps --yolo's policy on thread requests. Approval mode
// leaves both settings to Codex configuration and invocation overrides.
func (u *appServerUI) threadPermissions(params map[string]any) map[string]any {
	if !u.approvalMode {
		params["approvalPolicy"], params["sandbox"] = "never", "danger-full-access"
	}
	return params
}

// guardRequests receives shell decisions from the invocation approval server.
func (u *appServerUI) guardRequests() <-chan *vcsApproval {
	if u.guard == nil {
		return nil
	}
	return u.guard.requests
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
	u.approvals.Add(a.Approval)
	if u.approvals.handles == nil {
		u.approvals.handles = make(map[*prompt.Approval]*nativeApproval)
	}
	u.approvals.handles[a.Approval] = a
	u.recordApproval(a, "Pending Approval")
	u.approvals.AutoOpen = true
	if a.request == nil {
		u.notify("approval-requested", "Approval requested")
	}
	u.autoOpenApprovals()
}

func (u *appServerUI) addGuardApproval(request *vcsApproval) {
	if request.finished() {
		return
	}
	kind, known := binding.GuardForRequest(request.kind)
	grants := u.approvals.grants[kind]
	if !known || grants == nil {
		request.reply <- shellapproval.Reply{Reason: "approval module is not active"}
		return
	}
	details := u.approvalDirectory(request.cwd)
	details = append(details, "Denying fails only this command with exit status 1.")
	a := &nativeApproval{Approval: &prompt.Approval{Thread: request.thread, Item: request.item, Title: "Allow this remote write?", Subject: shellCommandString(request.argv[0], request.argv[1:]), Details: details, Choices: []prompt.Choice{
		{ID: 0, Label: "Yes, run it", Approve: true, Outcome: "Approved"},
		{ID: 1, Label: "Yes, for this exact command and workdir this session", Approve: true, Session: true, Outcome: "Approved for this session"},
		{ID: 2, Label: "No, fail this command", Deny: true, Outcome: "Denied"},
	}},
		guard: request}
	if request.kind == "sudo" {
		a.Title = "Allow this sudo command?"
	}
	// Match only the hook's exact host item. An environment-clearing wrapper
	// can omit the thread; an ambiguous item is never attributed by recency.
	if request.item != "" {
		if owner := u.approvalItem(request.thread, "", request.item); owner != nil {
			a.Thread, a.Turn, a.Item = owner.thread, owner.turn, owner.item
		}
	}
	// Password requests are never command approvals or session-grant lookups.
	if request.kind == "sudo-password" {
		u.openSudoPassword(a)
		return
	}
	if grants.Allows(guardApprovalIdentity(request)) {
		u.recordApproval(a, "Approved for this session")
		request.reply <- shellapproval.Reply{OK: true}
		return
	}
	u.addApproval(a)
}

// approvalMessage answers Codex's approval requests. Resolution and turn
// completion notifications also reach their other owners.
func (u *appServerUI) approvalMessage(m appserver.Message) (bool, error) {
	switch m.Method {
	case "item/autoApprovalReview/started", "item/autoApprovalReview/completed":
		review, err := binding.DecodeApprovalReview(m.Params)
		if err != nil {
			return true, err
		}
		record := conversationbinding.ApprovalReview(review)
		if record.Item != "" && record.Outcome != "" {
			u.recordApproval(&nativeApproval{Approval: &record}, record.Outcome)
		}
		return true, nil
	case "serverRequest/resolved":
		var p struct {
			ThreadID  string         `json:"threadId"`
			RequestID jsontext.Value `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			for _, a := range slices.Clone(u.pendingApprovals()) {
				if a.request != nil && string(a.request) == string(p.RequestID) && (p.ThreadID == "" || a.Thread == p.ThreadID) {
					u.endApproval(a, "Resolved elsewhere")
				}
			}
		}
		return false, nil
	case "turn/completed":
		var p struct {
			ThreadID string       `json:"threadId"`
			Turn     binding.Turn `json:"turn"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			for _, a := range slices.Clone(u.pendingApprovals()) {
				if a.request != nil && a.Thread == p.ThreadID && a.Turn == p.Turn.ID {
					u.endApproval(a, "Turn ended before an answer")
				}
			}
			if u.proxy != nil {
				u.proxy.control.ClearFeedback(p.ThreadID, p.Turn.ID)
			}
		}
		return false, nil
	case "item/started", "item/completed":
		var p struct {
			Item binding.ThreadItem `json:"item"`
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
	params, err := binding.DecodeApprovalRequest(m.Params)
	if err != nil {
		return true, fmt.Errorf("%s: %w", m.Method, err)
	}
	decoded, err := conversationbinding.DecodeApproval(m.Method, m.ID, params, u.session.cwd, u.approvals.patches[params.ItemID], u.proxy != nil)
	if err != nil {
		return true, err
	}
	if decoded == nil {
		return false, nil
	}
	a := &nativeApproval{Approval: &decoded.Record, request: slices.Clone(m.ID), responses: decoded.Responses}
	if decoded.DisplayCommand {
		a.Subject = appServerDisplayCommand(a.Subject)
	}
	u.blockNotification(m)
	u.addApproval(a)
	return true, nil
}

// expireApprovals withdraws guarded writes whose command stopped waiting.
func (u *appServerUI) expireApprovals() bool {
	changed := u.expireSudoPasswords()
	for _, a := range slices.Clone(u.pendingApprovals()) {
		if a.guard != nil && a.guard.finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if a.guard.outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			u.endApproval(a, outcome)
			changed = true
		}
	}
	return changed
}

// endApproval removes a pending approval and records how it ended.
func (u *appServerUI) endApproval(a *nativeApproval, outcome string) {
	index := slices.Index(u.pendingApprovals(), a)
	if index < 0 {
		return
	}
	wasOpen := u.approvals.Open && index == 0
	if wasOpen {
		u.hideApprovals()
	}
	u.approvals.Remove(a.Approval)
	delete(u.approvals.handles, a.Approval)
	if wasOpen {
		u.openApprovals()
	}
	if a.request != nil && u.notifications != nil {
		delete(u.notifications.blocked, string(a.request))
	}
	u.recordApproval(a, outcome)
}

// approvalItem finds one exact host identity across the two presentation views.
func (u *appServerUI) approvalItem(thread, turn, item string) *liveActivityNativeItem {
	var owner *liveActivityNativeItem
	for _, view := range []*activityAdapter{u.view, u.agents} {
		if view == nil {
			continue
		}
		for _, entry := range view.Entries {
			n := entry.Source.native
			if n == nil || n.item != item || thread != "" && n.thread != thread || turn != "" && n.turn != turn {
				continue
			}
			if owner != nil && !owner.sameItem(n) {
				return nil
			}
			owner = n
		}
	}
	return owner
}

// recordApproval updates the host item, never a separate approval event.
func (u *appServerUI) recordApproval(a *nativeApproval, outcome string) {
	if a.Item == "" {
		return
	}
	if (a.Subject == "" || a.guard != nil) && u.approvalItem(a.Thread, a.Turn, a.Item) == nil {
		if a.guard != nil {
			a.Outcome = outcome
			if !slices.Contains(u.approvals.unbound, a) {
				u.approvals.unbound = append(u.approvals.unbound, a)
			}
		}
		return
	}
	if a.guard != nil {
		owner := u.approvalItem(a.Thread, a.Turn, a.Item)
		a.Thread, a.Turn = owner.thread, owner.turn
		outcome += "\nCommand: " + a.Subject
	}
	entry := activityPaneEntry{Seq: u.session.next(), Kind: "tool", Text: activitypresentation.ShellSource(toolActivityShellSource(a.Subject), ""), Observed: u.now(),
		native: &liveActivityNativeItem{thread: a.Thread, turn: a.Turn, item: a.Item, phase: "approval/updated", approval: outcome}}
	if u.agents != nil {
		entry.Agent = u.session.path(a.Thread)
		u.applyActivity([]activityPaneEntry{entry}, nil)
	} else {
		entry.Seq, entry.Agent = u.view.LastSeq+1, "Main"
		u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{entry}})
	}
}

// flushApprovalUpdates reconciles the independent guard and host channels.
func (u *appServerUI) flushApprovalUpdates() {
	for i := 0; i < len(u.approvals.unbound); {
		a := u.approvals.unbound[i]
		if u.approvalItem(a.Thread, a.Turn, a.Item) == nil {
			i++
			continue
		}
		u.approvals.unbound = slices.Delete(u.approvals.unbound, i, i+1)
		u.recordApproval(a, a.Outcome)
	}
}

func (u *appServerUI) decideApproval(intent sessionpresentation.ApprovalIntent) error {
	record, choice, err := u.sessionPresentation().Approval(intent, u.approvals.Approvals)
	if err != nil {
		u.setNotice(err.Error(), true)
		return nil
	}
	return u.applyApproval(u.approvals.handles[record], choice, intent.Reason)
}

// applyApproval also settles decisions covered by an existing exact-command
// grant; those carry their admitted authority rather than a new renderer intent.
func (u *appServerUI) applyApproval(a *nativeApproval, choice prompt.Choice, reason string) error {
	reason = strings.TrimSpace(reason)
	feedback := "user denied this command without a reason"
	if reason != "" {
		feedback = "user denied this command with a reason: " + reason
	}
	if choice.Deny && reason != "" {
		choice.Outcome += ": " + reason
	}
	if a.guard != nil {
		if a.guard.finished() {
			u.expireApprovals() // The command already has its answer.
			return nil
		}
		reply := shellapproval.Reply{OK: choice.Approve}
		if !choice.Approve {
			reply.Reason = feedback
		} else if choice.Session {
			kind, _ := binding.GuardForRequest(a.guard.kind)
			u.approvals.grants[kind].Allow(guardApprovalIdentity(a.guard))
		}
		a.guard.reply <- reply
	} else {
		answer := func() error { return u.requests.Respond(a.request, a.responses[choice.ID]) }
		if choice.Deny && (u.proxy != nil || reason != "") {
			var observations *sessioncontrol.Observations
			if u.proxy != nil {
				observations = &u.proxy.control
			}
			if err := observations.AnswerWithFeedback(a.Thread, a.Turn, "Command: "+a.Subject+"\n"+feedback, answer); err != nil {
				return err
			}
		} else if err := answer(); err != nil {
			return err
		}
	}
	u.endApproval(a, choice.Outcome)
	if a.guard != nil && choice.Approve && choice.Session {
		for _, pending := range slices.Clone(u.pendingApprovals()) {
			if pending.guard != nil && pending.guard.kind == a.guard.kind && guardApprovalIdentity(pending.guard) == guardApprovalIdentity(a.guard) {
				if err := u.applyApproval(pending, prompt.Choice{Approve: true, Outcome: "Approved for this session"}, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// autoOpenApprovals takes over an empty, idle composer. Explicitly hiding the
// dock leaves it closed until reopened or another approval arrives.
func (u *appServerUI) autoOpenApprovals() {
	if u.approvals.ShouldOpen(u.composerVacant(), u.questions.Active != nil) {
		u.openApprovals()
	}
}

func (u *appServerUI) openApprovals() bool {
	if len(u.pendingApprovals()) == 0 {
		return false
	}
	if u.approvals.Open {
		return true
	}
	u.hideQuestions()
	if a := u.pendingApprovals()[0]; a.DenialChoice() >= 0 {
		u.mainView.PromptColumn = u.mainView.CursorColumn
		u.promptEditor.Open(u.Editor, a.Editor)
		u.mainView.CursorColumn = a.layout.Column
	}
	u.approvals.Show()
	u.approvals.painted = false
	u.focusPrompt()
	return true
}

func (u *appServerUI) hideApprovals() {
	if u.approvals.Open && len(u.pendingApprovals()) > 0 {
		if a := u.pendingApprovals()[0]; a.DenialChoice() >= 0 {
			a.layout.Column = u.mainView.CursorColumn
			a.Editor = u.promptEditor.Close(u.Editor)
			u.mainView.CursorColumn = u.mainView.PromptColumn
		}
	}
	u.approvals.Hide()
}

func (u *appServerUI) approvalKey(key string) (bool, error) {
	if !u.approvals.Open || len(u.approvals.Pending) == 0 {
		return false, nil
	}
	a := u.approvals.handles[u.approvals.Pending[0]]
	switch a.layout.Key(a.Approval, key, u.Text == "") {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		u.hideApprovals()
		u.approvals.AutoOpen = false
	case conversationui.PromptSubmit:
		view := u.mainPresentation(u.now())
		return true, u.decideApproval(sessionpresentation.ApprovalIntent{Version: view.Version, Request: a.ID, Choice: a.Choices[a.Selected].ID, Reason: u.Text})
	}
	return true, nil
}
