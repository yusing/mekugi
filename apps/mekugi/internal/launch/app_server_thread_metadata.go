package launch

import (
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Child event subscriptions do not imply a thread/started notification. Read
// metadata once, without loading history or resuming/executing the child.
func requestThreadMetadata(requests *sessioncontrol.Requests[appServerRequest], session *sessionpresentation.Attachment, thread string) error {
	path := session.KnownCaller(thread)
	known := path != "" && path != sessionpresentation.Placeholder(thread) && session.Agent(path).Role != "" && !session.Agent(path).AudiencePending
	return requests.ReadThreadMetadata("main", thread, known, appServerRequest{metadata: thread})
}

func applyThreadMetadata(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], reviewOwner *sessionreview.Review, approvals *nativeApprovalDock, receipts *[]*changepresentation.Receipt, clock func() time.Time, m appserver.Message, thread string) bool {
	if thread == "" {
		return false
	}
	if m.Error != nil {
		return true
	}
	info, err := binding.DecodeHistoryThread(m.Result, thread)
	if err != nil {
		return true
	} // Metadata cannot block observed activity.
	if control.RetiredThread(thread, info.ParentThreadID) {
		forgetSideActivity(attached, thread)
		applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, nil, attached.session.Roster())
		return true
	}
	old := attached.session.Caller(thread)
	registerThread(&attached.session, info)
	control.Requests.AdmitSideChild(thread, info.ParentThreadID)
	// Resolve descendants from authoritative parent identities, including metadata
	// arriving after their output. Unresolved rows remain in Activity only.
	for range len(attached.session.Roster()) {
		changed := false
		for _, observed := range attached.session.Roster() {
			agent := attached.session.Activity().Agent(observed.Thread)
			control.Requests.AdmitSideChild(observed.Thread, agent.ParentThread)
			side := control.Requests.Side(observed.Thread)
			parent := attached.session.Activity().Agent(agent.ParentThread)
			knownMain := agent.ParentThread == control.Thread || parent != nil && parent.ParentThread != "" && !parent.AudiencePending && !parent.Isolated
			if side == nil && !knownMain {
				continue
			}
			pending := agent.AudiencePending
			agent.AudiencePending, agent.Isolated = false, side != nil
			if side != nil && !side.Closed && observed.Thread != side.Thread && !strings.HasPrefix(agent.Name, side.Caller()+"/") {
				previous := agent.Name
				agent.Name = side.Caller() + strings.TrimPrefix(agent.Name, "/root")
				renameThreadActivity(attached, terminal, previous, agent.Name)
			}
			if side != nil && side.Closed {
				control.RetireThreads(side.Thread, observed.Thread)
				forgetSideActivity(attached, observed.Thread)
			} else if pending && side == nil {
				for _, row := range attached.childSource.Entries {
					entry := row.Source
					if entry.Native == nil || entry.Native.Thread != observed.Thread {
						continue
					}
					if projected, admitted := sessionpresentation.MainActivityEntry(entry); admitted {
						projected.ActivitySeq = entry.Seq
						attached.source.ApplyMain(projected)
					}
				}
			}
			changed = changed || pending
		}
		if !changed {
			break
		}
	}
	if agent := attached.session.Activity().Agent(thread); agent != nil && agent.AudiencePending && agent.ParentThread != "" {
		// Metadata lookup owns deduplication; this does not resume the parent.
		if err := requestThreadMetadata(control.Requests, &attached.session, agent.ParentThread); err != nil {
			setNotice(&attached.feedback, terminal, presentationTime(clock), err.Error(), true)
		}
	}
	if name := attached.session.KnownCaller(thread); name != "" {
		renameThreadActivity(attached, terminal, old, name)
	}
	applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, nil, attached.session.Roster())
	return true
}

// Late metadata updates canonical labels before detached terminal delivery.
func renameThreadActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, old, name string) {
	attached.presentation.RenameCaller(old, name)
	for _, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		source.RenameCaller(old, name)
	}
	terminalsession.RenameSelection(old, name, terminal.Transcript, terminal.Agents)
}
