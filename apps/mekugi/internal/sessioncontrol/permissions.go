package sessioncontrol

import (
	json "encoding/json/v2"
	"fmt"
	"reflect"
	"slices"
)

// PermissionState contains only host-confirmed thread policy.
type PermissionState struct {
	Active *struct {
		ID string `json:"id"`
	} `json:"activePermissionProfile"`
	Approval any    `json:"approvalPolicy"`
	Reviewer string `json:"approvalsReviewer"`
}

func permissionField(field string) bool {
	return field == "permissions" || field == "approvalPolicy" || field == "approvalsReviewer"
}

func (p PermissionState) matches(field string, value any) bool {
	switch field {
	case "permissions":
		return p.Active != nil && value == p.Active.ID
	case "approvalPolicy":
		return reflect.DeepEqual(value, p.Approval)
	case "approvalsReviewer":
		return value == p.Reviewer
	}
	return false
}

type PermissionProfile struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Allowed     bool   `json:"allowed"`
}

// PermissionSelection holds the input gate for one attached selection. Closing
// a view releases only that selection, never an admitted settings operation.
type PermissionSelection struct {
	Closed   bool
	Profiles []PermissionProfile
}

func (o *Settings) BeginPermissionSelection() *PermissionSelection {
	if o.Pending() {
		return nil
	}
	o.selection = new(PermissionSelection)
	return o.selection
}

func (s *PermissionSelection) Choose(profile string) (map[string]any, error) {
	if s == nil || s.Closed {
		return nil, fmt.Errorf("Permission selection expired · reopen /permissions")
	}
	index := slices.IndexFunc(s.Profiles, func(p PermissionProfile) bool { return p.ID == profile })
	if index < 0 || !s.Profiles[index].Allowed {
		return nil, fmt.Errorf("Permission profile unavailable under current requirements")
	}
	return permissionProfileChange(profile), nil
}

// ParsePermissionCommand preserves the host's named-profile and inline syntax.
// Source: codex-rs/tui/src/chatwidget/permission_discovery.rs:44:65 and
// permissions_menu.rs:48:99@0df4aa677.
func ParsePermissionCommand(args string) (map[string]any, error) {
	profile, options, err := settingsArguments(args, "--approval-policy", "--reviewer")
	if err != nil {
		return nil, err
	}
	if profile == "" {
		return nil, fmt.Errorf("Use /permissions PROFILE [--approval-policy=POLICY] [--reviewer=user|auto_review]")
	}
	change := permissionProfileChange(profile)
	if policy, ok := options["--approval-policy"]; ok {
		value, err := approvalPolicy(policy)
		if err != nil {
			return nil, err
		}
		change["approvalPolicy"] = value
	}
	if reviewer, ok := options["--reviewer"]; ok {
		if reviewer != "user" && reviewer != "auto_review" {
			return nil, fmt.Errorf("Use reviewer user or auto_review")
		}
		change["approvalsReviewer"] = reviewer
	}
	return change, nil
}

func permissionProfileChange(profile string) map[string]any {
	change := map[string]any{"permissions": profile}
	switch profile {
	case ":read-only", ":workspace", ":ask-for-approval", ":approve-for-me":
		change["approvalPolicy"], change["approvalsReviewer"] = "on-request", "user"
		if profile == ":ask-for-approval" || profile == ":approve-for-me" {
			change["permissions"] = ":workspace"
		}
		if profile == ":approve-for-me" {
			change["approvalsReviewer"] = "auto_review"
		}
	case ":danger-full-access":
		change["approvalPolicy"], change["approvalsReviewer"] = "never", "user"
	}
	return change
}

// Normalize host defaults before unchanged detection; Codex emits no settings
// notification when a request only spells the current policy differently.
// Source: codex-rs/protocol/src/protocol.rs:1097:1137@0df4aa677.
func approvalPolicy(text string) (any, error) {
	if text == "untrusted" || text == "on-request" || text == "never" {
		return text, nil
	}
	invalid := fmt.Errorf("Use approval policy untrusted, on-request, never, or a JSON granular policy with boolean sandbox_approval, rules and mcp_elicitations")
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, invalid
	}
	if name, ok := value.(string); ok {
		if name == "on-failure" {
			return "on-request", nil
		}
		if name == "untrusted" || name == "on-request" || name == "never" {
			return name, nil
		}
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return nil, invalid
	}
	granular, ok := object["granular"].(map[string]any)
	if !ok {
		return nil, invalid
	}
	for field, flag := range granular {
		if !slices.Contains([]string{"sandbox_approval", "rules", "mcp_elicitations", "skill_approval", "request_permissions"}, field) {
			return nil, invalid
		}
		if _, ok := flag.(bool); !ok {
			return nil, invalid
		}
	}
	for _, field := range []string{"sandbox_approval", "rules", "mcp_elicitations"} {
		if _, ok := granular[field]; !ok {
			return nil, invalid
		}
	}
	for _, field := range []string{"skill_approval", "request_permissions"} {
		if _, ok := granular[field]; !ok {
			granular[field] = false
		}
	}
	return object, nil
}
