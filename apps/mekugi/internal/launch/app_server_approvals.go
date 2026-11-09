package launch

import (
	"encoding/json/jsontext"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	json "encoding/json/v2"
	"fmt"
	"slices"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

type nativeApprovalDock struct {
	*prompt.Approvals
	handles map[*prompt.Approval]*sessioncontrol.Approval
	unbound []*sessioncontrol.Approval
	patches map[string][]string
	guards  []binding.ShellApproval
	allows  func(*sessioncontrol.ShellApprovalRequest) bool
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

func addApproval(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, a *sessioncontrol.Approval) {
	approvals.Add(a.Approval)
	if approvals.handles == nil {
		approvals.handles = make(map[*prompt.Approval]*sessioncontrol.Approval)
	}
	approvals.handles[a.Approval] = a
	recordApproval(attached, approvals, reviewOwner, receipts, control.Thread, clock, a, "Pending Approval")
	approvals.AutoOpen = true
	if a.RequestID == nil {
		notifications.notify(&attached.feedback, clock, "approval-requested", "Approval requested")
	}
	autoOpenApprovals(attached, picker, editor, questions, approvals, control.Thread, overlay)
}

func addGuardApproval(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, request *sessioncontrol.ShellApprovalRequest) {
	if request.Finished() {
		return
	}
	kind, known := binding.GuardForRequest(request.Kind)
	if !known || !slices.ContainsFunc(approvals.guards, func(guard binding.ShellApproval) bool { return guard.Kind == kind }) {
		request.Reply <- shellapproval.Reply{Reason: "approval module is not active"}
		return
	}
	details := approvalDirectory(attached.cwd, request.Cwd)
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
		if owner := approvalItem(attached, request.Thread, "", request.Item); owner != nil {
			a.Thread, a.Turn, a.Item = owner.Thread, owner.Turn, owner.Item
		}
	}
	// Password requests are never command approvals or session-grant lookups.
	if request.Kind == "sudo-password" || request.Kind == "gpg-passphrase" {
		openSudoPassword(attached, control, picker, editor, questions, approvals, clock, overlay, notifications, a)
		return
	}
	if approvals.allows != nil && approvals.allows(request) {
		recordApproval(attached, approvals, reviewOwner, receipts, control.Thread, clock, a, "Approved for this session")
		request.Reply <- shellapproval.Reply{OK: true}
		return
	}
	addApproval(attached, control, picker, editor, questions, approvals, clock, overlay, notifications, reviewOwner, receipts, a)
}

func addDelegateApproval(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, request *sessioncontrol.DelegateApprovalRequest) {
	if request.Finished() {
		return
	}
	attached.session.RegisterThread(request.Source)
	addApproval(attached, control, picker, editor, questions, approvals, clock, overlay, notifications, reviewOwner, receipts, &sessioncontrol.Approval{Approval: request.Approval, Delegate: request})
}

// approvalMessage answers Codex's approval requests. Resolution and turn
// completion notifications also reach their other owners.
func approvalMessage(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, overlay bool, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, observations *sessioncontrol.Observations, m appserver.Message) (bool, error) {
	switch m.Method {
	case "item/autoApprovalReview/started", "item/autoApprovalReview/completed":
		review, err := binding.DecodeApprovalReview(m.Params)
		if err != nil {
			return true, err
		}
		record := conversationbinding.ApprovalReview(review)
		if record.Item != "" && record.Outcome != "" {
			recordApproval(attached, approvals, reviewOwner, receipts, control.Thread, clock, &sessioncontrol.Approval{Approval: &record}, record.Outcome)
		}
		return true, nil
	case "serverRequest/resolved":
		var p struct {
			ThreadID  string         `json:"threadId"`
			RequestID jsontext.Value `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			for _, a := range slices.Clone(approvals.calls()) {
				if a.RequestID != nil && string(a.RequestID) == string(p.RequestID) && (p.ThreadID == "" || a.Thread == p.ThreadID) {
					endApproval(attached, control, picker, editor, questions, approvals, clock, notifications, reviewOwner, receipts, a, "Resolved elsewhere")
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
			for _, a := range slices.Clone(approvals.calls()) {
				if a.RequestID != nil && a.Thread == p.ThreadID && a.Turn == p.Turn.ID {
					endApproval(attached, control, picker, editor, questions, approvals, clock, notifications, reviewOwner, receipts, a, "Turn ended before an answer")
				}
			}
			if observations != nil {
				observations.ClearFeedback(p.ThreadID, p.Turn.ID)
			}
		}
		return false, nil
	case "item/started", "item/completed":
		var p struct {
			Item binding.ThreadItem `json:"item"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.Item.Type == "fileChange" {
			if approvals.patches == nil {
				approvals.patches = make(map[string][]string)
			}
			if m.Method == "item/started" {
				var paths []string
				for _, change := range p.Item.Changes {
					paths = append(paths, pathdisplay.ForWorkspace(attached.cwd, change.Path))
				}
				approvals.patches[p.Item.ID] = paths
			} else {
				delete(approvals.patches, p.Item.ID)
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
	decoded, err := conversationbinding.DecodeApproval(m.Method, m.ID, params, attached.cwd, approvals.patches[params.ItemID], observations != nil)
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
	notifications.blockNotification(&attached.feedback, clock, m)
	addApproval(attached, control, picker, editor, questions, approvals, clock, overlay, notifications, reviewOwner, receipts, a)
	return true, nil
}

// expireApprovals withdraws guarded writes whose command stopped waiting.
func expireApprovals(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt) bool {
	changed := expireSudoPasswords(attached, control, editor, questions, approvals, clock, reviewOwner, receipts)
	for _, a := range slices.Clone(approvals.calls()) {
		if a.Delegate != nil && a.Delegate.Finished() {
			endApproval(attached, control, picker, editor, questions, approvals, clock, notifications, reviewOwner, receipts, a, "Withdrawn: the request stopped")
			changed = true
			continue
		}
		if a.Guard != nil && a.Guard.Finished() {
			outcome := "Auto Denied: no answer within 5 minutes"
			if a.Guard.Outcome == "withdrawn" {
				outcome = "Withdrawn: the command stopped"
			}
			endApproval(attached, control, picker, editor, questions, approvals, clock, notifications, reviewOwner, receipts, a, outcome)
			changed = true
		}
	}
	return changed
}

// endApproval removes a pending approval and records how it ended.
func endApproval(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, notifications *nativeNotifications, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, a *sessioncontrol.Approval, outcome string) {
	index := slices.Index(approvals.calls(), a)
	if index < 0 {
		return
	}
	wasOpen := approvals.Open && index == 0
	if wasOpen {
		hideApprovals(approvals, &attached.conversation.PromptEditor, &attached.conversation.Editor)
	}
	approvals.Remove(a.Approval)
	delete(approvals.handles, a.Approval)
	if wasOpen && !attached.presentation.DisplayingSide() {
		openApprovals(approvals, questions, attached.conversation, picker, editor, control.Thread)
	}
	if a.RequestID != nil && notifications != nil {
		delete(notifications.blocked, string(a.RequestID))
	}
	recordApproval(attached, approvals, reviewOwner, receipts, control.Thread, clock, a, outcome)
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
func recordApproval(attached *attachedPresentation, approvals *nativeApprovalDock, review *sessionreview.Review, pending *[]*changepresentation.Receipt, main string, clock func() time.Time, a *sessioncontrol.Approval, outcome string) {
	entry, ok := approvalRecord(attached, approvals, main, presentationTime(clock), a, outcome)
	if !ok {
		return
	}
	if attached.session.KnownCaller(main) != "" {
		applyActivity(attached, approvals, review, pending, main, clock, []sessionpresentation.ActivityEntry{entry}, nil)
	} else {
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
	}
}

// approvalRecord binds only an exact host identity. Early guard outcomes stay
// with the existing approval dock until that item arrives.
func approvalRecord(attached *attachedPresentation, approvals *nativeApprovalDock, main string, at time.Time, a *sessioncontrol.Approval, outcome string) (sessionpresentation.ActivityEntry, bool) {
	if a.Item == "" {
		return sessionpresentation.ActivityEntry{}, false
	}
	owner := approvalItem(attached, a.Thread, a.Turn, a.Item)
	if (a.Subject == "" || a.Guard != nil) && owner == nil {
		if a.Guard != nil {
			a.Outcome = outcome
			if !slices.Contains(approvals.unbound, a) {
				approvals.unbound = append(approvals.unbound, a)
			}
		}
		return sessionpresentation.ActivityEntry{}, false
	}
	if a.Guard != nil {
		a.Thread, a.Turn = owner.Thread, owner.Turn
		outcome += "\nCommand: " + a.Subject
	}
	entry := sessionpresentation.ActivityEntry{Seq: attached.session.Activity().NextSequence(), Kind: "tool", Text: activitypresentation.ShellSource(frontends.ShellDisplaySource(a.Subject), ""), Observed: at,
		Native: &sessionpresentation.NativeItem{Thread: a.Thread, Turn: a.Turn, Item: a.Item, Command: a.Subject, Phase: "approval/updated", Approval: outcome}}
	if attached.session.KnownCaller(main) != "" {
		entry.Agent = attached.session.Caller(a.Thread)
	} else {
		entry.Seq, entry.Agent = attached.source.LastSeq+1, "Main"
	}
	return entry, true
}

func (session *nativeSession) decideApproval(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, side *sessioncontrol.Side, clock func() time.Time, overlay bool, intent sessionpresentation.ApprovalIntent) error {
	defer func() {
		settlePresentation(attached, &session.Client, picker, editor, session.settings, session.serviceTiers, session.title, session.backendVersion, session.restoring != nil, side, clock, &session.questions, &session.approvals, overlay)
	}()
	record, choice, err := attached.presentation.Approval(intent, session.approvals.Approvals)
	if err != nil {
		setNotice(&attached.feedback, presentationTime(clock), err.Error(), true)
		return nil
	}
	return session.applyApproval(attached, picker, editor, clock, attached.grants, session.approvals.handles[record], choice, intent.Reason)
}

// applyApproval also settles decisions covered by an existing exact-command
// grant; those carry their admitted authority rather than a new renderer intent.
func (session *nativeSession) applyApproval(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, clock func() time.Time, grants sessioncontrol.ApprovalGrants, a *sessioncontrol.Approval, choice prompt.Choice, reason string) error {
	outcome, delivered, err := a.Decide(choice, reason, grants, session.Client.Requests, session.controlObservations)
	if err != nil {
		return err
	}
	if !delivered {
		expireApprovals(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.notifications, session.review, &session.pendingEditReceipts)
		return nil
	}
	endApproval(attached, &session.Client, picker, editor, &session.questions, &session.approvals, clock, session.notifications, session.review, &session.pendingEditReceipts, a, outcome)
	if a.Guard != nil && choice.Approve && choice.Session {
		for _, pending := range slices.Clone(session.approvals.calls()) {
			if a.SameCommand(pending) {
				if err := session.applyApproval(attached, picker, editor, clock, grants, pending, prompt.Choice{Approve: true, Outcome: "Approved for this session"}, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// autoOpenApprovals takes over an empty, idle composer. Explicitly hiding the
// dock leaves it closed until reopened or another approval arrives.
func autoOpenApprovals(attached *attachedPresentation, picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, main string, overlay bool) {
	if approvals.ShouldOpen(composerVacant((*editor), picker, attached.presentation, overlay || approvals.Open), questions.Active != nil) {
		openApprovals(approvals, questions, attached.conversation, picker, editor, main)
	}
}

func openApprovals(approvals *nativeApprovalDock, questions *nativeQuestionDock, conversation *conversationmodel.Session, picker *composerPicker, editor **composer.Editor, main string) bool {
	if len(approvals.calls()) == 0 {
		return false
	}
	if approvals.Open {
		return true
	}
	*editor = hideQuestionEditor(questions, conversation, *editor, main)
	if a := approvals.calls()[0]; a.DenialChoice() >= 0 {
		conversation.PromptEditor.Open(*editor, a.Editor)
	}
	approvals.Show()
	focusPrompt(picker)
	return true
}

func hideApprovals(approvals *nativeApprovalDock, parked *prompt.Editor, editor *composer.Editor) {
	if approvals.Open && len(approvals.calls()) > 0 {
		if a := approvals.calls()[0]; a.DenialChoice() >= 0 {
			a.Editor = parked.Close(editor)
		}
	}
	approvals.Hide()
}
