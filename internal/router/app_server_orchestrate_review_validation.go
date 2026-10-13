package router

import (
	"context"
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
	params := map[string]any{"command": []string{"bash", "-c", check.Command}, "processId": work.dispatch.ID, "cwd": child.batch.Cwd, "disableTimeout": true}
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
	if err := u.orchestrateRPCRequest(orchestrateRPC{method: "command/exec", child: child, review: work}, params); err != nil {
		u.endWorkflowWork(work)
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
	}
}

func (u *appServerUI) workflowCheckResponse(work *orchestrateReviewWork, m appserver.Message) {
	child, c, name, d := work.child, work.child.command, work.child.batch.TaskName, work.dispatch
	u.endWorkflowWork(work)
	if u.workflowWorkStopped(work) {
		return
	}
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
			result.Output = workflowOutputTail(strings.TrimSpace(response.Stdout + "\n" + response.Stderr))
		}
	}
	u.workflowStore(child, func(ctx context.Context, s *orchestrate.Store) error {
		return s.RecordWorkflowCheck(ctx, c.workspace, c.main, name, result)
	}, func(err error) {
		if err != nil && !work.canceled {
			u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
		}
	})
}

func workflowOutputTail(output string) string {
	if len(output) <= workflowCheckOutputLimit {
		return output
	}
	return strings.ToValidUTF8(output[len(output)-workflowCheckOutputLimit:], "")
}
