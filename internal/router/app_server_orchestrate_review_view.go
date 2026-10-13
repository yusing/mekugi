package router

import (
	json "encoding/json/v2"
	"fmt"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

// Reviewers are independent host threads, not implementation batches or native
// descendants. Their views observe work; only the workflow can submit turns.
type workflowReviewView struct {
	name     string
	restored bool
}

func (u *appServerUI) addWorkflowReviewView(child *orchestrateChild, d orchestrate.WorkflowDispatch, raw []byte, restored bool, ready func()) error {
	if err := u.addOrchestratedView(child, d.ThreadID, raw, ready); err != nil {
		return err
	}
	v := u.navigation.views[d.ThreadID]
	v.awaitingTurn = false
	v.status = "Ready"
	v.workflowReview = &workflowReviewView{name: fmt.Sprintf("%s/%s/%d", child.batch.TaskName, d.Kind, d.Generation), restored: restored}
	if restored {
		v.awaitingTurn, v.status = false, d.HostStatus
		if v.status == "" || v.status == "running" {
			v.status = "interrupted"
		}
		v.restoreUsage(appServerThreadInfo{ID: d.ThreadID})
		agent := v.session.agent("/root")
		agent.Final = d.HostStatus == "completed"
		v.observeCost(d.ThreadID, agent)
		if d.HostStatus == "failed" {
			u.workflowReviewError(d.ThreadID, "Recorded review failed")
		}
	}
	return nil
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
	u.restoreActivityThread(result.Thread)
	u.restoreMainHistory(result.Thread.Turns, nil, nil)
	u.dirty = true
	return true
}
