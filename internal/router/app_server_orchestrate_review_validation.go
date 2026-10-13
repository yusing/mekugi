package router

import (
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

// workflowCheckOutputLimit bounds retained check output for review and correction.
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
		entry := activityPaneEntry{Seq: v.session.next(), Observed: u.now(), Agent: "/root", Kind: "tool", Text: "Run " + commentaryCode(check.Command), CallID: work.dispatch.ID,
			native: &liveActivityNativeItem{thread: v.thread, item: work.dispatch.ID, command: check.Command, commandCwd: child.batch.Cwd, phase: "command/exec", running: true, commandStarted: u.now(), output: v.session.outputs.New()}}
		v.session.commands[[3]string{v.thread, "", work.dispatch.ID}] = &appServerCommandRun{entry: entry}
		v.applyActivity([]activityPaneEntry{entry}, nil)
	}
	if err := u.orchestrateRPCRequest(orchestrateRPC{method: "command/exec", child: child, review: work}, params); err != nil {
		u.finishWorkflowCheckDisplay(work, "failed", err.Error(), nil)
		u.endWorkflowWork(work)
		u.workflowFailure(child, orchestrate.PhaseNeedsDecision, err)
	}
}

func (u *appServerUI) workflowCheckResponse(work *orchestrateReviewWork, m appserver.Message) {
	child, c, name, d := work.child, work.child.command, work.child.batch.TaskName, work.dispatch
	u.endWorkflowWork(work)
	result := orchestrate.CheckResult{Generation: d.Generation, Phase: orchestrate.PhaseValidation, Command: work.check.Command, Cwd: child.batch.Cwd, Revision: d.Revision, CheckID: work.check.ID, Outcome: orchestrate.CheckUncertain}
	var response struct {
		ExitCode *int `json:"exitCode"`
	}
	decodeErr := json.Unmarshal(m.Result, &response)
	output := work.decodeCheckOutput(0, "", true) + work.decodeCheckOutput(1, "", true)
	switch {
	case m.Error != nil:
		result.Output = "command/exec: " + m.Error.Message
	case decodeErr != nil || response.ExitCode == nil:
		result.Output = "host returned no exit status"
	default:
		result.ExitCode = response.ExitCode
		result.Outcome = orchestrate.CheckPassed
		result.Output = workflowOutputTail(strings.TrimSpace(work.checkOutput + output))
		if *response.ExitCode != 0 {
			result.Outcome = orchestrate.CheckFailed
		}
	}
	status := "completed"
	if result.Outcome != orchestrate.CheckPassed {
		status = string(result.Outcome)
	}
	if result.ExitCode == nil {
		output = result.Output
	}
	if u.workflowWorkStopped(work) {
		status = "interrupted"
	}
	u.finishWorkflowCheckDisplay(work, status, output, result.ExitCode)
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
		Stream      string `json:"stream"`
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
		stream := 0
		if event.Stream == "stderr" {
			stream = 1
		} else if event.Stream != "stdout" {
			u.setNotice("Validation output has an unknown stream", true)
			return true
		}
		output := work.decodeCheckOutput(stream, string(data), event.CapReached)
		if event.CapReached {
			output += "\n[host output cap reached]\n"
		}
		u.appendWorkflowCheckOutput(work, output)
		return true
	}
	return false
}

// Keep incomplete rune bytes with their stream before merging output for display.
func (work *orchestrateReviewWork) decodeCheckOutput(stream int, data string, final bool) string {
	data = work.checkUTF8[stream] + data
	end := len(data)
	if !final {
		for i := len(data) - 1; i >= max(0, len(data)-utf8.UTFMax+1); i-- {
			if utf8.RuneStart(data[i]) {
				if !utf8.FullRuneInString(data[i:]) {
					end = i
				}
				break
			}
		}
	}
	work.checkUTF8[stream] = data[end:]
	return strings.ToValidUTF8(data[:end], "�")
}

func (u *appServerUI) appendWorkflowCheckOutput(work *orchestrateReviewWork, output string) {
	work.checkOutput += output
	work.checkOutput = work.checkOutput[max(0, len(work.checkOutput)-workflowCheckOutputLimit):]
	if n := u.navigation; n != nil {
		if v := n.views[work.child.batch.Launch.ThreadID]; v != nil {
			if run := v.session.commands[[3]string{v.thread, "", work.dispatch.ID}]; run != nil {
				run.output.Write(output)
				run.entry.native.output.Write(output)
				run.dirty = true
			}
		}
	}
}

func (u *appServerUI) finishWorkflowCheckDisplay(work *orchestrateReviewWork, status, output string, exit *int) {
	u.appendWorkflowCheckOutput(work, output)
	if u.navigation == nil {
		return
	}
	v := u.navigation.views[work.child.batch.Launch.ThreadID]
	if v == nil {
		return
	}
	key := [3]string{v.thread, "", work.dispatch.ID}
	if run := v.session.commands[key]; run != nil {
		entry, native := run.entry, *run.entry.native
		entry.native, entry.Seq = &native, v.session.next()
		native.running, native.commandEnded, native.status, native.phase = false, u.now(), status, "command/exec/completed"
		native.output.Finish(nil, exit)
		run.done = v.session.commandDone(entry, appServerItem{Type: "commandExecution", AggregatedOutput: &work.checkOutput, ExitCode: exit}, u.now())
		if status == "interrupted" || exit == nil {
			kind, text := "error", "Validation "+status+": "+output
			if status == "interrupted" {
				kind, text = "progress", "Validation canceled"
			}
			run.done = append(run.done, activityPaneEntry{Seq: v.session.next(), Agent: "/root", Kind: kind, Text: text, Observed: u.now()})
		}
		v.flushStreamOutput()
	}
}

func workflowOutputTail(output string) string {
	output = strings.ToValidUTF8(output, "")
	if len(output) <= workflowCheckOutputLimit {
		return output
	}
	return strings.ToValidUTF8(output[len(output)-workflowCheckOutputLimit:], "")
}
