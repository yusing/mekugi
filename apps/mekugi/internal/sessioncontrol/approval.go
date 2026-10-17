package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/contracts/approval"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// Approval binds a Conversation prompt to its live response authority.
// It is not a published view and contains no renderer state.
type Approval struct {
	*prompt.Approval
	RequestID jsontext.Value
	Responses []map[string]any
	Guard     *ShellApprovalRequest
}

type ApprovalGrants struct {
	values map[binding.ShellGuardKind]binding.CommandGrants
}

func NewApprovalGrants(guards []binding.ShellApproval) ApprovalGrants {
	g := ApprovalGrants{values: make(map[binding.ShellGuardKind]binding.CommandGrants, len(guards))}
	for _, guard := range guards {
		g.values[guard.Kind] = guard.NewSession()
	}
	return g
}

func (g ApprovalGrants) Available(request *ShellApprovalRequest) bool {
	kind, known := binding.GuardForRequest(request.Kind)
	return known && g.values[kind] != nil
}

func commandApprovalIdentity(request *ShellApprovalRequest) approval.Command {
	command, _ := json.Marshal(request.Argv)
	return approval.Command{Directory: request.Cwd, Executable: request.Executable, Arguments: string(command)}
}

func (g ApprovalGrants) Allows(request *ShellApprovalRequest) bool {
	kind, known := binding.GuardForRequest(request.Kind)
	return known && g.values[kind] != nil && g.values[kind].Allows(commandApprovalIdentity(request))
}

func (a *Approval) SameCommand(other *Approval) bool {
	return a.Guard != nil && other.Guard != nil && a.Guard.Kind == other.Guard.Kind && commandApprovalIdentity(a.Guard) == commandApprovalIdentity(other.Guard)
}

// Decide delivers one already-admitted choice. False means the guard request
// has expired; its presentation must reconcile expiry instead of recording a reply.
// Source: apps/mekugi/internal/router/app_server_approvals.go:351:382@c8cec41a.
func (a *Approval) Decide(choice prompt.Choice, reason string, grants ApprovalGrants, transport Responder, observations *Observations) (outcome string, delivered bool, err error) {
	reason = strings.TrimSpace(reason)
	feedback := "user denied this command without a reason"
	if reason != "" {
		feedback = "user denied this command with a reason: " + reason
	}
	if choice.Deny && reason != "" {
		choice.Outcome += ": " + reason
	}
	if a.Guard != nil {
		if a.Guard.Finished() {
			return "", false, nil
		}
		reply := shellapproval.Reply{OK: choice.Approve}
		if !choice.Approve {
			reply.Reason = feedback
		} else if choice.Session {
			kind, _ := binding.GuardForRequest(a.Guard.Kind)
			grants.values[kind].Allow(commandApprovalIdentity(a.Guard))
		}
		a.Guard.Reply <- reply
	} else {
		answer := func() error { return transport.Respond(a.RequestID, a.Responses[choice.ID]) }
		if choice.Deny && (observations != nil || reason != "") {
			if err := observations.AnswerWithFeedback(a.Thread, a.Turn, "Command: "+a.Subject+"\n"+feedback, answer); err != nil {
				return "", false, err
			}
		} else if err := answer(); err != nil {
			return "", false, err
		}
	}
	return choice.Outcome, true, nil
}
