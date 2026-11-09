package sessioncontrol

import (
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

// HookTrust retains the exact opaque approval value returned by Codex. Never
// recompute it: Codex owns declaration and plugin-content trust semantics.
type HookTrust struct {
	Key, Hash string
	Saving    bool
}

// HookTrustApprovals projects Codex's startup inventory into the shared approval
// dock. Disabled hooks may be reviewed without changing their enablement.
func HookTrustApprovals(data []byte, cwd string) ([]*Approval, error) {
	var result struct {
		Data []struct {
			Cwd   string `json:"cwd"`
			Hooks []struct {
				Key         string `json:"key"`
				CurrentHash string `json:"currentHash"`
				TrustStatus string `json:"trustStatus"`
				SourcePath  string `json:"sourcePath"`
				EventName   string `json:"eventName"`
				HandlerType string `json:"handlerType"`
				Command     string `json:"command"`
				Server      string `json:"server"`
				Tool        string `json:"tool"`
				Matcher     string `json:"matcher"`
				Enabled     bool   `json:"enabled"`
			} `json:"hooks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	var approvals []*Approval
	for _, entry := range result.Data {
		if entry.Cwd != cwd {
			continue
		}
		for _, hook := range entry.Hooks {
			if hook.TrustStatus != "untrusted" && hook.TrustStatus != "modified" {
				continue
			}
			if hook.Key == "" || hook.CurrentHash == "" {
				return nil, fmt.Errorf("hook inventory omitted trust identity")
			}
			subject := hook.Command
			if subject == "" {
				subject = strings.TrimSpace(hook.HandlerType + " " + hook.Server + " " + hook.Tool)
			}
			details := []string{"Trust: " + hook.TrustStatus, "Source: " + pathdisplay.ForWorkspace(cwd, hook.SourcePath), "Event: " + hook.EventName}
			if hook.Matcher != "" {
				details = append(details, "Matcher: "+hook.Matcher)
			}
			if !hook.Enabled {
				details = append(details, "Disabled; trusting does not enable this hook.")
			}
			details = append(details, "Hooks can execute commands. Trust only hooks you have reviewed.")
			approvals = append(approvals, &Approval{Hook: &HookTrust{Key: hook.Key, Hash: hook.CurrentHash}, Approval: &prompt.Approval{
				Title: "Trust this hook?", Subject: subject, Details: details,
				Choices: []prompt.Choice{
					{ID: 0, Label: "Continue without trusting", Outcome: "Not trusted"},
					{ID: 1, Label: "Trust this hook", Approve: true, Outcome: "Trusted"},
				},
			}})
		}
	}
	return approvals, nil
}

// TrustHook persists only the reviewed hash, preserving other hooks and enabled
// flags. The response must succeed before startup proceeds or the prompt closes.
func (r *Requests[T]) TrustHook(a *Approval, owner T) error {
	if a.Hook.Saving {
		return nil
	}
	_, err := r.Send("config/batchWrite", map[string]any{
		"edits":            []any{map[string]any{"keyPath": "hooks.state", "value": map[string]any{a.Hook.Key: map[string]any{"trusted_hash": a.Hook.Hash}}, "mergeStrategy": "upsert"}},
		"reloadUserConfig": true,
	}, "", owner)
	if err == nil {
		a.Hook.Saving = true
	}
	return err
}
