package rollout

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// Runtime is an exact admitted turn-context record, not a reconstructed policy
// from the coarse sandbox label or current global configuration.
type Runtime struct {
	Turn                    string         `json:"turn_id"`
	Workspace               string         `json:"cwd"`
	Model                   string         `json:"model"`
	Effort                  *string        `json:"effort"`
	Summary                 *string        `json:"summary"`
	ApprovalPolicy          jsontext.Value `json:"approval_policy"`
	ApprovalsReviewer       string         `json:"approvals_reviewer"`
	PermissionProfile       jsontext.Value `json:"permission_profile"`
	ActivePermissionProfile *struct {
		ID string `json:"id"`
	} `json:"active_permission_profile"`
	SandboxPolicy     jsontext.Value `json:"sandbox_policy"`
	CollaborationMode jsontext.Value `json:"collaboration_mode"`
}

// Admission is the host's durable canonical settings, distinct from the coarse
// sandbox summary in app-server thread/start and settings notifications.
type Admission struct {
	Model             string         `json:"model"`
	Provider          string         `json:"model_provider_id"`
	Workspace         string         `json:"cwd"`
	RuntimeRoots      []string       `json:"runtime_workspace_roots"`
	Effort            *string        `json:"reasoning_effort"`
	Summary           *string        `json:"reasoning_summary"`
	Tier              *string        `json:"service_tier"`
	PermissionProfile jsontext.Value `json:"permission_profile"`
	ApprovalPolicy    jsontext.Value `json:"approval_policy"`
	ApprovalsReviewer string         `json:"approvals_reviewer"`
}

// ReadAdmission requires a snapshot owned by this thread. Inherited snapshots
// cannot confirm a newly created child's policy, even in an admitted rollout.
func ReadAdmission(thread, path string) (Admission, error) {
	for line := range reverseRecords(thread, path) {
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				Type     string    `json:"type"`
				Thread   string    `json:"thread_id"`
				Settings Admission `json:"thread_settings"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "event_msg" || record.Payload.Type != "thread_settings_applied" || record.Payload.Thread != thread {
			continue
		}
		snapshot := record.Payload.Settings
		if snapshot.Model == "" || snapshot.Provider == "" || snapshot.Workspace == "" || snapshot.PermissionProfile.Kind() != '{' || !snapshot.PermissionProfile.IsValid() || (snapshot.ApprovalPolicy.Kind() != '"' && snapshot.ApprovalPolicy.Kind() != '{') || !snapshot.ApprovalPolicy.IsValid() {
			return Admission{}, errors.New("host admission settings have incomplete execution authority")
		}
		return snapshot, nil
	}
	return Admission{}, errors.New("host-owned admission settings are unavailable in the admitted rollout")
}

// ReadRuntime never substitutes a neighboring turn when the invoking turn is
// missing, partially written, or outside the bounded identity-validated tail.
// Source: codex-rs/protocol/src/protocol.rs:3426 TurnContextItem
// @0df4aa67781ae45a3c97befa4db48b734afe46d9.
func ReadRuntime(thread, path, turn string) (Runtime, error) {
	if thread == "" || turn == "" {
		return Runtime{}, errors.New("runtime snapshot requires thread and turn identity")
	}
	for line := range reverseRecords(thread, path) {
		var record struct {
			Type    string  `json:"type"`
			Payload Runtime `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "turn_context" || record.Payload.Turn != turn {
			continue
		}
		runtime := record.Payload
		if runtime.Model == "" || runtime.Workspace == "" || runtime.PermissionProfile.Kind() != '{' || !runtime.PermissionProfile.IsValid() || (runtime.ApprovalPolicy.Kind() != '"' && runtime.ApprovalPolicy.Kind() != '{') || !runtime.ApprovalPolicy.IsValid() {
			return Runtime{}, errors.New("admitted turn context has incomplete execution authority")
		}
		return runtime, nil
	}
	return Runtime{}, errors.New("invoking turn context is unavailable in the admitted rollout")
}
