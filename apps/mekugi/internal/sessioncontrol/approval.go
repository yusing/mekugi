package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// Approval binds a Conversation prompt to its live response authority.
// It is not a published view and contains no renderer state.
type Approval struct {
	*prompt.Approval
	RequestID  jsontext.Value
	Responses  []map[string]any
	ShellGuard *binding.ShellGuardApprovalParams
	Hook       *HookTrust
}

type commandIdentity struct {
	Directory, Executable, Arguments string
}

// ApprovalGrants stores attachment-scoped exact-command grants for native
// `shellGuard/requestApproval` prompts.
type ApprovalGrants struct {
	values map[binding.ShellGuardKind]map[commandIdentity]bool
}

func NewApprovalGrants() ApprovalGrants {
	return ApprovalGrants{
		values: map[binding.ShellGuardKind]map[commandIdentity]bool{
			binding.RemoteWrite: make(map[commandIdentity]bool),
			binding.Sudo:        make(map[commandIdentity]bool),
		},
	}
}

func commandApprovalIdentity(request *binding.ShellGuardApprovalParams) commandIdentity {
	command, _ := json.Marshal(request.Argv)
	return commandIdentity{Directory: request.Cwd, Executable: request.Executable, Arguments: string(command)}
}

func (g ApprovalGrants) Allows(request *binding.ShellGuardApprovalParams) bool {
	return request != nil && g.values[request.Kind] != nil && g.values[request.Kind][commandApprovalIdentity(request)]
}

func (a *Approval) SameCommand(other *Approval) bool {
	return a.ShellGuard != nil && other.ShellGuard != nil && a.ShellGuard.Kind == other.ShellGuard.Kind && commandApprovalIdentity(a.ShellGuard) == commandApprovalIdentity(other.ShellGuard)
}

// Decide delivers one already-admitted choice through the active JSON-RPC transport.
func (a *Approval) Decide(choice prompt.Choice, reason string, grants ApprovalGrants, transport Responder, observations *Observations) (outcome string, delivered bool, err error) {
	reason = strings.TrimSpace(reason)
	feedback := "user denied this command without a reason"
	if reason != "" {
		feedback = "user denied this command with a reason: " + reason
	}
	if choice.Deny && reason != "" {
		choice.Outcome += ": " + reason
	}
	if a.ShellGuard != nil {
		response := binding.ShellGuardApprovalResponse{Decision: "decline"}
		if choice.Approve {
			if choice.Session {
				response.Decision = "acceptForSession"
			} else {
				response.Decision = "accept"
			}
		} else {
			response.Reason = &feedback
		}
		if err := transport.Respond(a.RequestID, response); err != nil {
			return "", false, err
		}
		if choice.Approve && choice.Session && grants.values[a.ShellGuard.Kind] != nil {
			grants.values[a.ShellGuard.Kind][commandApprovalIdentity(a.ShellGuard)] = true
		}
		return choice.Outcome, true, nil
	}
	answer := func() error { return transport.Respond(a.RequestID, a.Responses[choice.ID]) }
	if choice.Deny && (observations != nil || reason != "") {
		if err := observations.AnswerWithFeedback(a.Thread, a.Turn, "Command: "+a.Subject+"\n"+feedback, answer); err != nil {
			return "", false, err
		}
	} else if err := answer(); err != nil {
		return "", false, err
	}
	return choice.Outcome, true, nil
}
