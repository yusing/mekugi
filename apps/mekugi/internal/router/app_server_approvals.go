package router

import (
	"encoding/json/jsontext"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	json "encoding/json/v2"
	"fmt"
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/prompt"

	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

type nativeApprovalDock struct {
	*prompt.Approvals
	handles map[*prompt.Approval]*sessioncontrol.Approval
	unbound []*sessioncontrol.Approval
	patches map[string][]string
	grants  sessioncontrol.ApprovalGrants
}

func (d *nativeApprovalDock) calls() []*sessioncontrol.Approval {
	result := make([]*sessioncontrol.Approval, 0, len(d.Pending))
	for _, a := range d.Pending {
		result = append(result, d.handles[a])
	}
	return result
}

// approvalDirectory names a request's directory unless it is the workspace.
func approvalDirectory(workspace string, cwd string) []string {
	if cwd == "" {
		return nil
	}
	if path := pathdisplay.ForWorkspace(workspace, cwd); path != "." {
		return []string{"in " + path}
	}
	return nil
}

func (u *appServerUI) addApproval(a *sessioncontrol.Approval) {
	u.approvals.Add(a.Approval)
	if u.approvals.handles == nil {
		u.approvals.handles = make(map[*prompt.Approval]*sessioncontrol.Approval)
	}
	u.approvals.handles[a.Approval] = a
	u.recordApproval(a, "Pending Approval")
	u.approvals.AutoOpen = true
	if a.RequestID == nil {
		u.notify("approval-requested", "Approval requested")
	}
	u.autoOpenApprovals()
}

func (u *appServerUI) addGuardApproval(request *sessioncontrol.ShellApprovalRequest) {
	if request.Finished() {
		return
	}
	if !u.approvals.grants.Available(request) {
		request.Reply <- shellapproval.Reply{Reason: "approval module is not active"}
		return
	}
	details := approvalDirectory(u.attached.cwd, request.Cwd)
	details = append(details, "Denying fails only this command with exit status 1.")
	a := &sessioncontrol.Approval{Approval: &prompt.Approval{Thread: request.Thread, Item: request.Item, Title: "Allow this remote write?", Subject: shellCommandString(request.Argv[0], request.Argv[1:]), Details: details, Choices: []prompt.Choice{
		{ID: 0, Label: "Yes, run it", Approve: true, Outcome: "Approved"},
		{ID: 1, Label: "Yes, for this exact command and workdir this session", Approve: true, Session: true, Outcome: "Approved for this session"},
		{ID: 2, Label: "No, fail this command", Deny: true, Outcome: "Denied"},
	}},
		Guard: request}
	if request.Kind == "sudo" {
		a.Title = "Allow this sudo command?"
	}
	// Match only the hook's exact host item. An environment-clearing wrapper
	// can omit the thread; an ambiguous item is never attributed by recency.
	if request.Item != "" {
		if owner := approvalItem(&u.attached, request.Thread, "", request.Item); owner != nil {
			a.Thread, a.Turn, a.Item = owner.Thread, owner.Turn, owner.Item
		}
	}
	// Password requests are never command approvals or session-grant lookups.
	if request.Kind == "sudo-password" {
		u.openSudoPassword(a)
		return
	}
	if u.approvals.grants.Allows(request) {
		u.recordApproval(a, "Approved for this session")
		request.Reply <- shellapproval.Reply{OK: true}
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
			u.recordApproval(&sessioncontrol.Approval{Approval: &record}, record.Outcome)
		}
		return true, nil
	case "serverRequest/resolved":
		var p struct {
			ThreadID  string         `json:"threadId"`
			RequestID jsontext.Value `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			for _, a := range slices.Clone(u.approvals.calls()) {
				if a.RequestID != nil && string(a.RequestID) == string(p.RequestID) && (p.ThreadID == "" || a.Thread == p.ThreadID) {
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
			for _, a := range slices.Clone(u.approvals.calls()) {
				if a.RequestID != nil && a.Thread == p.ThreadID && a.Turn == p.Turn.ID {
					u.endApproval(a, "Turn ended before an answer")
				}
			}
			if u.controlObservations != nil {
				u.controlObservations.ClearFeedback(p.ThreadID, p.Turn.ID)
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
					paths = append(paths, pathdisplay.ForWorkspace(u.attached.cwd, change.Path))
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
	decoded, err := conversationbinding.DecodeApproval(m.Method, m.ID, params, u.attached.cwd, u.approvals.patches[params.ItemID], u.controlObservations != nil)
	if err != nil {
		return true, err
	}
	if decoded == nil {
		return false, nil
	}
	a := &sessioncontrol.Approval{Approval: &decoded.Record, RequestID: slices.Clone(m.ID), Responses: decoded.Responses}
	if decoded.DisplayCommand {
		a.Subject = sessionpresentation.DisplayCommand(a.Subject)
	}
	u.blockNotification(m)
	u.addApproval(a)
	return true, nil
}

// expireApprovals withdraws guarded writes whose command stopped waiting.
func (u *appServerUI) expireApprovals() bool {
	changed := u.expireSudoPasswords()
	for _, a := range slices.Clone(u.approvals.calls()) {
		if a.Guard != nil && a.Guard.Finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if a.Guard.Outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			u.endApproval(a, outcome)
			changed = true
		}
	}
	return changed
}

// endApproval removes a pending approval and records how it ended.
func (u *appServerUI) endApproval(a *sessioncontrol.Approval, outcome string) {
	index := slices.Index(u.approvals.calls(), a)
	if index < 0 {
		return
	}
	wasOpen := u.approvals.Open && index == 0
	if wasOpen {
		hideApprovals(&u.approvals, u.shell, &u.attached.conversation.PromptEditor, u.Editor)
	}
	u.approvals.Remove(a.Approval)
	delete(u.approvals.handles, a.Approval)
	u.shell.Prompts.ForgetApproval(a.ID)
	if wasOpen {
		openApprovals(&u.approvals, &u.questions, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread)
	}
	if a.RequestID != nil && u.notifications != nil {
		delete(u.notifications.blocked, string(a.RequestID))
	}
	u.recordApproval(a, outcome)
}

// approvalItem finds one exact host identity across the two presentation views.
func approvalItem(attached *attachedPresentation, thread, turn, item string) *sessionpresentation.NativeItem {
	var owner *sessionpresentation.NativeItem
	for _, view := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		if view == nil {
			continue
		}
		for _, entry := range view.Entries {
			n := entry.Source.Native
			if n == nil || n.Item != item || thread != "" && n.Thread != thread || turn != "" && n.Turn != turn {
				continue
			}
			if owner != nil && !owner.SameItem(n) {
				return nil
			}
			owner = n
		}
	}
	return owner
}

// recordApproval updates the host item, never a separate approval event.
func (u *appServerUI) recordApproval(a *sessioncontrol.Approval, outcome string) {
	if a.Item == "" {
		return
	}
	if (a.Subject == "" || a.Guard != nil) && approvalItem(&u.attached, a.Thread, a.Turn, a.Item) == nil {
		if a.Guard != nil {
			a.Outcome = outcome
			if !slices.Contains(u.approvals.unbound, a) {
				u.approvals.unbound = append(u.approvals.unbound, a)
			}
		}
		return
	}
	if a.Guard != nil {
		owner := approvalItem(&u.attached, a.Thread, a.Turn, a.Item)
		a.Thread, a.Turn = owner.Thread, owner.Turn
		outcome += "\nCommand: " + a.Subject
	}
	entry := sessionpresentation.ActivityEntry{Seq: u.attached.session.Activity().NextSequence(), Kind: "tool", Text: activitypresentation.ShellSource(frontends.ShellDisplaySource(a.Subject), ""), Observed: presentationTime(u.clock),
		Native: &sessionpresentation.NativeItem{Thread: a.Thread, Turn: a.Turn, Item: a.Item, Phase: "approval/updated", Approval: outcome}}
	if u.attached.session.KnownCaller(u.Thread) != "" {
		entry.Agent = u.attached.session.Caller(a.Thread)
		u.applyActivity([]sessionpresentation.ActivityEntry{entry}, nil)
	} else {
		entry.Seq, entry.Agent = u.attached.source.LastSeq+1, "Main"
		u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
	}
}

// flushApprovalUpdates reconciles the independent guard and host channels.
func (u *appServerUI) flushApprovalUpdates() {
	for i := 0; i < len(u.approvals.unbound); {
		a := u.approvals.unbound[i]
		if approvalItem(&u.attached, a.Thread, a.Turn, a.Item) == nil {
			i++
			continue
		}
		u.approvals.unbound = slices.Delete(u.approvals.unbound, i, i+1)
		u.recordApproval(a, a.Outcome)
	}
}

func (u *appServerUI) decideApproval(intent sessionpresentation.ApprovalIntent) error {
	defer u.settlePresentation()
	record, choice, err := u.attached.presentation.Approval(intent, u.approvals.Approvals)
	if err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return nil
	}
	return u.applyApproval(u.approvals.handles[record], choice, intent.Reason)
}

// applyApproval also settles decisions covered by an existing exact-command
// grant; those carry their admitted authority rather than a new renderer intent.
func (u *appServerUI) applyApproval(a *sessioncontrol.Approval, choice prompt.Choice, reason string) error {
	outcome, delivered, err := a.Decide(choice, reason, u.approvals.grants, u.Requests, u.controlObservations)
	if err != nil {
		return err
	}
	if !delivered {
		u.expireApprovals()
		return nil
	}
	u.endApproval(a, outcome)
	if a.Guard != nil && choice.Approve && choice.Session {
		for _, pending := range slices.Clone(u.approvals.calls()) {
			if a.SameCommand(pending) {
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
	if u.approvals.ShouldOpen(composerVacant(u.Editor, u.shell, &u.picker, u.attached.presentation, u.statusPanel != nil || u.resumePicker != nil || u.approvals.Open), u.questions.Active != nil) {
		openApprovals(&u.approvals, &u.questions, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread)
	}
}

func openApprovals(approvals *nativeApprovalDock, questions *nativeQuestionDock, conversation *conversationmodel.Session, terminal *terminalsession.Attachment, picker *composerPicker, editor **composer.Editor, main string) bool {
	if len(approvals.calls()) == 0 {
		return false
	}
	if approvals.Open {
		return true
	}
	*editor = hideQuestionEditor(questions, conversation, terminal, *editor, main)
	if a := approvals.calls()[0]; a.DenialChoice() >= 0 {
		terminal.Main.PromptColumn = terminal.Main.CursorColumn
		conversation.PromptEditor.Open(*editor, a.Editor)
		terminal.Main.CursorColumn = terminal.Prompts.ApprovalLayout(a.ID).Column
	}
	approvals.Show()
	terminal.Prompts.Reopen()
	focusPrompt(picker, terminal)
	return true
}

func hideApprovals(approvals *nativeApprovalDock, terminal *terminalsession.Attachment, parked *prompt.Editor, editor *composer.Editor) {
	if approvals.Open && len(approvals.calls()) > 0 {
		if a := approvals.calls()[0]; a.DenialChoice() >= 0 {
			terminal.Prompts.ApprovalLayout(a.ID).Column = terminal.Main.CursorColumn
			a.Editor = parked.Close(editor)
			terminal.Main.CursorColumn = terminal.Main.PromptColumn
		}
	}
	approvals.Hide()
}

func (u *appServerUI) applyApprovalInput(input terminalsession.PromptInput) (bool, error) {
	keyInput := input.PromptKey
	if err := u.attached.presentation.ApprovalInput(sessionpresentation.ApprovalInputIntent{Version: input.Version, ApprovalInput: keyInput.Approval}, u.approvals.Approvals); err != nil {
		setNotice(&u.attached.feedback, u.shell, presentationTime(u.clock), err.Error(), true)
		return true, nil
	}
	a := u.approvals.handles[u.approvals.Pending[0]]
	switch keyInput.Action {
	case conversationui.PromptPass:
		return false, nil
	case conversationui.PromptHide:
		hideApprovals(&u.approvals, u.shell, &u.attached.conversation.PromptEditor, u.Editor)
		u.approvals.AutoOpen = false
	case conversationui.PromptSubmit:
		view := u.attached.presentation.View()
		return true, u.decideApproval(sessionpresentation.ApprovalIntent{Version: view.Version, Request: a.ID, Choice: a.Choices[a.Selected].ID, Reason: u.Text})
	}
	return true, nil
}
