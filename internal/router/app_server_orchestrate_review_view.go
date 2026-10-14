package router

import (
	json "encoding/json/v2"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

// Reviewers are independent host threads, not implementation batches or native
// descendants. Their views observe work; only the workflow can submit turns.
type workflowReviewView struct {
	*workflowReviewerReset
	name     string
	restored bool
}

func (u *appServerUI) addWorkflowReviewView(child *orchestrateChild, d orchestrate.WorkflowDispatch, raw []byte, restored bool, ready func()) error {
	u.proxy.mu.Lock()
	if u.proxy.reviewerResets == nil {
		u.proxy.reviewerResets = make(map[string]*workflowReviewerReset)
	}
	ref := u.proxy.reviewerResets[d.ThreadID]
	if ref == nil {
		ref = &workflowReviewerReset{run: journalRun{Directory: u.proxy.orchestration.store.Directory, Workspace: child.command.workspace, Main: child.command.main}, batch: child.batch.TaskName, dispatch: d.ID}
		u.proxy.reviewerResets[d.ThreadID] = ref
	}
	u.proxy.mu.Unlock()
	if err := u.addOrchestratedView(child, d.ThreadID, raw, ready); err != nil {
		return err
	}
	v := u.navigation.views[d.ThreadID]
	v.awaitingTurn = false
	v.status = "Ready"
	v.workflowReview = &workflowReviewView{workflowReviewerReset: ref, name: child.batch.TaskName + "/" + string(d.Kind), restored: restored}
	if restored {
		status := d.HostStatus
		if d.State == orchestrate.DispatchFailed {
			status = "failed"
		} else if status == "" || status == "running" {
			status = "interrupted"
		}
		v.restoreUsage(appServerThreadInfo{ID: d.ThreadID})
		v.observeCost(d.ThreadID, v.session.agent("/root"))
		v.restoreWorkflowReviewStatus(status)
	}
	return nil
}

// Recorded terminal status updates presentation without starting a live clock.
func (u *appServerUI) restoreWorkflowReviewStatus(status string) {
	u.awaitingTurn, u.turn, u.status = false, "", status
	agent := u.session.agent("/root")
	agent.Final, agent.Responding = status == "completed", false
	agent.WorkTimer.update(false, u.now())
	u.applyActivity(nil, u.session.agents)
	if status == "failed" {
		u.workflowReviewError(u.thread, "Recorded review failed")
	}
	u.dirty = true
}

func (u *appServerUI) workflowStartingReviewer() bool {
	for _, r := range u.orchestrateRequests {
		if r.review != nil && r.method == "thread/start" {
			return true
		}
	}
	// The acknowledgement can be waiting for its durable dispatch receipt.
	for _, child := range u.orchestrateThreads {
		for _, work := range child.workflowWork {
			if work.dispatch.ThreadID != "" && work.dispatch.ThreadID != child.batch.Launch.ThreadID && (u.navigation == nil || u.navigation.views[work.dispatch.ThreadID] == nil) {
				return true
			}
		}
	}
	return false
}

func (u *appServerUI) workflowReviewError(thread, detail string) {
	if u.navigation == nil {
		return
	}
	if v := u.navigation.views[thread]; v != nil && v.workflowReview != nil {
		v.awaitingTurn, v.status = false, "failed"
		v.applyActivity([]activityPaneEntry{{Seq: v.session.next(), Agent: "/root", Kind: "error", Text: detail, Observed: u.now()}}, nil)
	}
}

// Selecting retained review history reads it without resuming the thread or
// reviving a turn. Provider accounting restores through its existing owner.
func (u *appServerUI) workflowReviewHistory(m appserver.Message) bool {
	if u.workflowReview == nil || m.Method != "" || u.requests[string(m.ID)] != "workflow/review/read" {
		return false
	}
	delete(u.requests, string(m.ID))
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	if m.Error != nil || json.Unmarshal(m.Result, &result) != nil || result.Thread.ID != u.thread || result.Thread.Cwd != u.session.cwd {
		u.workflowReview.restored = true
		u.setNotice("Review history unavailable", true)
		return true
	}
	u.registerSessionThread(result.Thread)
	if u.turn == "" {
		u.restoreActivityThread(result.Thread)
	}
	u.restoreMainTranscript(result.Thread.Turns, nil, nil)
	u.dirty = true
	return true
}
