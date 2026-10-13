package router

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

// workflowCheckOutputLimit bounds the retained output tail of a rejected check.
const workflowCheckOutputLimit = 4096

// startWorkflowCheck runs the next required check without a retained result
// through host command execution in the batch checkout, with the implementer's
// permissions. Only the host exit status decides the outcome.
func (u *appServerUI) startWorkflowCheck(child *orchestrateChild) {
	w := child.batch.Workflow
	i := slices.IndexFunc(w.Assignment.RequiredChecks, func(check orchestrate.RequiredCheck) bool {
		return !slices.ContainsFunc(w.Checks, func(r orchestrate.CheckResult) bool { return r.CheckID == check.ID })
	})
	if i < 0 || w.Candidate == nil {
		return
	}
	if child.turn != "" {
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, errors.New("implementer has unexpected active work"))
		return
	}
	check := w.Assignment.RequiredChecks[i]
	work := &orchestrateReviewWork{child: child, check: check, dispatch: orchestrate.WorkflowDispatch{
		ID:         fmt.Sprintf("workflow/%s/%d/check/%s", child.batch.TaskName, w.Generation, check.ID),
		Kind:       workflowValidation,
		Generation: w.Generation,
		Revision:   w.Candidate.Revision,
	}}
	params := map[string]any{"command": []string{"bash", "-c", check.Command}, "processId": work.dispatch.ID, "cwd": child.batch.Cwd, "disableTimeout": true, "streamStdoutStderr": true}
	if len(child.batch.Launch.SandboxPolicy) != 0 {
		params["sandboxPolicy"] = child.batch.Launch.SandboxPolicy
	} else {
		var launch struct {
			Permissions string `json:"permissions"`
		}
		if json.Unmarshal(child.batch.Launch.Params, &launch) == nil && launch.Permissions != "" {
			params["permissionProfile"] = launch.Permissions
		}
	}
	u.setWorkflowWork(child, work)
	if n := u.navigation; n != nil && n.views[child.batch.Launch.ThreadID] != nil {
		v := n.views[child.batch.Launch.ThreadID]
		work.checkEntry = activityPaneEntry{Agent: "/root", Kind: "tool", Text: "Run " + commentaryCode(check.Command), CallID: work.dispatch.ID,
			native: &liveActivityNativeItem{thread: v.thread, item: work.dispatch.ID, command: check.Command, commandCwd: child.batch.Cwd, phase: "command/exec", running: true, commandStarted: u.now(), output: v.session.outputs.New()}}
		u.showWorkflowCheck(work, "", "", nil)
	}
	if err := u.orchestrateRPCRequest(orchestrateRPC{method: "command/exec", child: child, review: work}, params); err != nil {
		u.showWorkflowCheck(work, "failed", err.Error(), nil)
		u.endWorkflowWork(work)
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
	}
}

func (u *appServerUI) workflowCheckResponse(work *orchestrateReviewWork, m appserver.Message) {
	child, c, name, d := work.child, work.child.command, work.child.batch.TaskName, work.dispatch
	u.endWorkflowWork(work)
	result := orchestrate.CheckResult{Generation: d.Generation, Phase: orchestrate.PhaseValidation, Command: work.check.Command, Cwd: child.batch.Cwd, Revision: d.Revision, CheckID: work.check.ID, Outcome: orchestrate.CheckUncertain}
	var response struct {
		ExitCode *int   `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	switch {
	case m.Error != nil:
		result.Output = "command/exec: " + m.Error.Message
	case json.Unmarshal(m.Result, &response) != nil || response.ExitCode == nil:
		result.Output = "host returned no exit status"
	default:
		result.ExitCode = response.ExitCode
		result.Outcome = orchestrate.CheckPassed
		if *response.ExitCode != 0 {
			result.Outcome = orchestrate.CheckFailed
			result.Output = workflowOutputTail(strings.TrimSpace(work.checkOutput + response.Stdout + "\n" + response.Stderr))
		}
	}
	status := "completed"
	if result.Outcome != orchestrate.CheckPassed {
		status = string(result.Outcome)
	}
	output := response.Stdout + response.Stderr
	if result.ExitCode == nil {
		output = result.Output
	}
	u.showWorkflowCheck(work, status, output, result.ExitCode)
	if u.workflowWorkStopped(work) {
		return
	}
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		return s.RecordWorkflowCheck(ctx, c.workspace, c.main, name, result)
	}, func(err error) {
		if err != nil && !work.canceled {
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
		}
	})
}

// command/exec belongs to the batch's validation phase. Its process identity
// updates a command row, never a model turn or usage counter.
func (u *appServerUI) workflowCheckMessage(m appserver.Message) bool {
	if m.Method != "command/exec/outputDelta" {
		return false
	}
	var event struct {
		ProcessID   string `json:"processId"`
		DeltaBase64 string `json:"deltaBase64"`
		CapReached  bool   `json:"capReached"`
	}
	if json.Unmarshal(m.Params, &event) != nil {
		return false
	}
	for _, request := range u.orchestrateRequests {
		work := request.review
		if request.method != "command/exec" || work == nil || work.dispatch.ID != event.ProcessID {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(event.DeltaBase64)
		if err != nil {
			u.setNotice("Validation output unavailable: "+err.Error(), true)
			return true
		}
		output := string(data)
		if event.CapReached {
			output += "\n[host output cap reached]\n"
		}
		work.checkOutput += output
		work.checkOutput = work.checkOutput[max(0, len(work.checkOutput)-workflowCheckOutputLimit):]
		if work.checkEntry.native != nil {
			work.checkEntry.native.output.Write(output)
			u.showWorkflowCheck(work, "", "", nil)
		}
		return true
	}
	return false
}

func (u *appServerUI) showWorkflowCheck(work *orchestrateReviewWork, outcome, output string, exit *int) {
	if work.checkEntry.native == nil || u.navigation == nil {
		return
	}
	v := u.navigation.views[work.child.batch.Launch.ThreadID]
	entry := work.checkEntry
	native := *entry.native
	entry.native, entry.Seq, entry.Observed = &native, v.session.next(), u.now()
	if outcome != "" {
		native.running, native.commandEnded, native.status = false, u.now(), outcome
		native.phase = "command/exec/completed"
		native.output.Write(output)
		work.checkOutput += output
		work.checkOutput = work.checkOutput[max(0, len(work.checkOutput)-workflowCheckOutputLimit):]
		native.output.Finish(nil, exit)
	}
	entry.outputTail, entry.outputOmit = appServerOutputTail(&work.checkOutput)
	v.applyActivity([]activityPaneEntry{entry}, nil)
	v.dirty = true
}

func workflowOutputTail(output string) string {
	output = strings.ToValidUTF8(output, "")
	if len(output) <= workflowCheckOutputLimit {
		return output
	}
	return strings.ToValidUTF8(output[len(output)-workflowCheckOutputLimit:], "")
}
