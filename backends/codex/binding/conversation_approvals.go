// Source: apps/mekugi/internal/router/app_server_approvals.go:265:500@[42afca9c] Codex approval protocol and stock choice semantics.
package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"
)

type ApprovalDecision struct {
	Label                  string
	Response               map[string]any
	Approve, Session, Deny bool
	Outcome                string
}
type ApprovalRequest struct {
	Kind           string `json:"kind"`
	ThreadID       string `json:"threadId"`
	TurnID         string `json:"turnId"`
	ItemID         string `json:"itemId"`
	Reason         string `json:"reason"`
	Command        string `json:"command"`
	Cwd            string `json:"cwd"`
	GrantRoot      string `json:"grantRoot"`
	NetworkContext *struct {
		Host string `json:"host"`
	} `json:"networkApprovalContext"`
	Additional         jsontext.Value   `json:"additionalPermissions"`
	Amendment          []string         `json:"proposedExecpolicyAmendment"`
	Permissions        jsontext.Value   `json:"permissions"`
	AvailableDecisions []jsontext.Value `json:"availableDecisions"`
}

type ApprovalReview struct {
	Thread, Turn, Item, Status string
}

// DecodeApprovalReview names the exact reviewed item, never the review item.
func DecodeApprovalReview(params jsontext.Value) (ApprovalReview, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"targetItemId"`
		Review   struct {
			Status string `json:"status"`
		} `json:"review"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ApprovalReview{}, err
	}
	return ApprovalReview{Thread: p.ThreadID, Turn: p.TurnID, Item: p.ItemID, Status: p.Review.Status}, nil
}

func DecodeApprovalRequest(params jsontext.Value) (ApprovalRequest, error) {
	var p ApprovalRequest
	err := json.Unmarshal(params, &p)
	return p, err
}

// CommandApprovalChoice keeps the host's approval scopes. Command rejection uses
// native decline instead of aborting before denial feedback reaches the provider.
func CommandApprovalChoice(decision jsontext.Value, network, permissions bool) (ApprovalDecision, bool) {
	choice := ApprovalDecision{Response: map[string]any{"decision": decision}}
	var name string
	if json.Unmarshal(decision, &name) == nil {
		switch name {
		case "accept":
			choice.Label, choice.Outcome = "Yes, proceed", "Approved"
			if network {
				choice.Label = "Yes, just this once"
			}
		case "acceptForSession":
			choice.Label, choice.Outcome = "Yes, and don't ask again for this command in this session", "Approved for this session"
			if network {
				choice.Label = "Yes, and allow this host for this conversation"
			} else if permissions {
				choice.Label = "Yes, and allow these permissions for this session"
			}
		case "decline":
			choice.Deny = true
			choice.Label, choice.Outcome = "No, continue without running it", "Declined"
		case "cancel":
			choice.Deny = true
			// Native decline rejects the command without aborting the turn,
			// so its denial and feedback reach the same provider continuation.
			choice.Response = map[string]any{"decision": "decline"}
			choice.Label, choice.Outcome = "No, continue without running it", "Declined"
		default:
			return choice, false
		}
		return choice, true
	}
	var variant map[string]struct {
		Command []string `json:"execpolicy_amendment"`
		Network *struct {
			Host   string `json:"host"`
			Action string `json:"action"`
		} `json:"network_policy_amendment"`
	}
	if json.Unmarshal(decision, &variant) != nil || len(variant) != 1 {
		return choice, false
	}
	for name, value := range variant {
		switch {
		case name == "acceptWithExecpolicyAmendment" && len(value.Command) > 0:
			quoted := make([]string, len(value.Command))
			for i, arg := range value.Command {
				quoted[i] = commandargs.QuoteArgument(arg)
			}
			prefix := strings.Join(quoted, " ")
			if strings.ContainsAny(prefix, "\r\n") {
				return choice, false
			}
			choice.Label = "Yes, and don't ask again for commands that start with `" + prefix + "`"
			choice.Outcome = "Approved commands starting with " + prefix
		case name == "applyNetworkPolicyAmendment" && value.Network != nil:
			if value.Network.Action == "deny" {
				choice.Deny = true
				choice.Label, choice.Outcome = "No, and block this host in the future", "Blocked "+value.Network.Host
			} else {
				choice.Label, choice.Outcome = "Yes, and allow this host in the future", "Allowed "+value.Network.Host
			}
		default:
			return choice, false
		}
	}
	return choice, true
}

// PermissionSummary condenses a requested permission profile, following the
// stock overlay's rule text.
// Source: codex-rs/tui/src/bottom_pane/approval_overlay.rs
// format_additional_permissions_rule @7135b303d.
func PermissionSummary(value jsontext.Value) string {
	var profile struct {
		Network *struct {
			Enabled *bool `json:"enabled"`
		} `json:"network"`
		FileSystem *struct {
			Read    []string `json:"read"`
			Write   []string `json:"write"`
			Entries []struct {
				Access string         `json:"access"`
				Path   jsontext.Value `json:"path"`
			} `json:"entries"`
		} `json:"fileSystem"`
	}
	if len(value) == 0 || json.Unmarshal(value, &profile) != nil {
		return ""
	}
	var parts []string
	if profile.Network != nil && profile.Network.Enabled != nil && *profile.Network.Enabled {
		parts = append(parts, "network")
	}
	if fs := profile.FileSystem; fs != nil {
		access := map[string][]string{"read": fs.Read, "write": fs.Write}
		for _, entry := range fs.Entries {
			var path struct {
				Path    string `json:"path"`
				Pattern string `json:"pattern"`
				Value   any    `json:"value"`
			}
			label := ""
			if json.Unmarshal(entry.Path, &label) != nil && json.Unmarshal(entry.Path, &path) == nil {
				switch {
				case path.Pattern != "":
					label = "glob " + path.Pattern
				case path.Path != "":
					label = path.Path
				case path.Value != nil:
					label = fmt.Sprint(path.Value)
				}
			}
			if label != "" {
				access[entry.Access] = append(access[entry.Access], label)
			}
		}
		for _, mode := range []string{"read", "write", "deny"} {
			if len(access[mode]) > 0 {
				label := mode
				if mode == "deny" {
					label = "deny read"
				}
				parts = append(parts, label+" "+strings.Join(access[mode], ", "))
			}
		}
	}
	return strings.Join(parts, "; ")
}
