// Source: apps/mekugi/internal/router/app_server_approvals.go:280:370@[42afca9c] native approval mapping.
package codexbinding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"mvdan.cc/sh/v3/shell"
)

type Approval struct {
	Record         prompt.Approval
	Responses      []map[string]any
	DisplayCommand bool
}

func DecodeApproval(method string, id jsontext.Value, p binding.ApprovalRequest, workspace string, paths []string, feedback bool) (*Approval, error) {
	a := prompt.Approval{ID: binding.QuestionIdentity(p.ThreadID, p.ItemID, id), Thread: p.ThreadID, Turn: p.TurnID, Item: p.ItemID}
	if p.Cwd != "" {
		if path := pathdisplay.ForWorkspace(workspace, p.Cwd); path != "." {
			a.Details = []string{"in " + path}
		}
	}
	var responses []map[string]any
	displayCommand := false
	appendChoice := func(c binding.ApprovalDecision) {
		a.Choices = append(a.Choices, prompt.Choice{ID: len(responses), Label: c.Label, Approve: c.Approve, Session: c.Session, Deny: c.Deny, Outcome: c.Outcome})
		responses = append(responses, c.Response)
	}
	switch method {
	case "item/commandExecution/requestApproval":
		a.Title, a.Subject = "Run this command?", p.Command
		displayCommand = true
		if p.NetworkContext != nil {
			a.Title = "Allow network access to " + p.NetworkContext.Host + "?"
		}
		if p.Kind == "writeStdin" {
			// Codex shell-joins write_stdin --session-id ID INPUT.
			// Source: codex-rs/core/src/tools/approvals.rs ApprovalAction::WriteStdin @7135b303d.
			a.Title = "Send input to the running terminal?"
			displayCommand = false
			if words, err := shell.Fields(p.Command, func(string) string { return "" }); err == nil && len(words) == 4 && words[0] == "write_stdin" {
				a.Title, a.Subject = "Send input to terminal "+words[2]+"?", "Input: "+strconv.Quote(words[3])
			}
		}
		if summary := binding.PermissionSummary(p.Additional); summary != "" {
			a.Details = append(a.Details, "Permissions: "+summary)
		}
		decisions := p.AvailableDecisions
		if len(decisions) == 0 {
			// Codex's default when the request names no decisions.
			decisions = []jsontext.Value{jsontext.Value(`"accept"`)}
			if len(p.Amendment) > 0 {
				amendment, _ := json.Marshal(map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": p.Amendment}})
				decisions = append(decisions, amendment)
			}
			decisions = append(decisions, jsontext.Value(`"cancel"`))
		}
		for _, decision := range decisions {
			if choice, ok := binding.CommandApprovalChoice(decision, p.NetworkContext != nil, len(p.Additional) > 0 && string(p.Additional) != "null"); ok {
				if !feedback && choice.Deny {
					// Proxy-free UI cannot attach feedback to provider requests.
					// Keep native decisions and omit the reason editor there.
					choice.Deny = false
					choice.Response = map[string]any{"decision": decision}
					if string(decision) == `"cancel"` {
						choice.Label = "No, and tell Codex what to do differently"
					}
				}
				if !choice.Deny || !slices.ContainsFunc(a.Choices, func(c prompt.Choice) bool { return c.Deny && c.Label == choice.Label }) {
					appendChoice(choice)
				}
			}
		}
	case "item/fileChange/requestApproval":
		a.Title = "Make these edits?"
		a.Subject = strings.Join(paths, "\n")
		if a.Subject == "" {
			a.Subject = "Proposed edits"
		}
		if p.GrantRoot != "" {
			a.Details = append(a.Details, "Allow writes under "+pathdisplay.ForWorkspace(workspace, p.GrantRoot)+" for this session")
		}
		for _, choice := range []struct{ Label, decision, Outcome string }{
			{"Yes, proceed", "accept", "Approved edits"},
			{"Yes, and don't ask again for these files", "acceptForSession", "Approved edits for this session"},
			{"No, and tell Codex what to do differently", "cancel", "Declined edits"},
		} {
			appendChoice(binding.ApprovalDecision{Label: choice.Label, Response: map[string]any{"decision": choice.decision}, Outcome: choice.Outcome})
		}
	case "item/permissions/requestApproval":
		a.Title, a.Subject = "Grant these permissions?", binding.PermissionSummary(p.Permissions)
		if a.Subject == "" {
			a.Subject = "Additional permissions"
		}
		granted := p.Permissions
		if len(granted) == 0 {
			granted = jsontext.Value(`{}`)
		}
		for _, choice := range []binding.ApprovalDecision{
			{Label: "Yes, grant these permissions for this turn", Response: map[string]any{"permissions": granted, "scope": "turn"}, Outcome: "Granted permissions for this turn"},
			{Label: "Yes, grant for this turn with strict auto review", Response: map[string]any{"permissions": granted, "scope": "turn", "strictAutoReview": true}, Outcome: "Granted permissions with strict auto review"},
			{Label: "Yes, grant these permissions for this session", Response: map[string]any{"permissions": granted, "scope": "session"}, Outcome: "Granted permissions for this session"},
			{Label: "No, continue without permissions", Response: map[string]any{"permissions": map[string]any{}, "scope": "turn"}, Outcome: "Denied permissions"},
		} {
			appendChoice(choice)
		}
	}
	if p.Reason != "" {
		a.Details = append(a.Details, "Reason: "+p.Reason)
	}
	if len(a.Choices) == 0 {
		return nil, nil // Leave an unanswerable request visibly blocked.
	}
	return &Approval{Record: a, Responses: responses, DisplayCommand: displayCommand}, nil
}
