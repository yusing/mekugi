package rollout

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"iter"
	"slices"
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
	ActivePermissionProfile *struct {
		ID string `json:"id"`
	} `json:"active_permission_profile,omitempty"`
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
	f, stat, ok := Open(thread, path)
	if !ok {
		return Admission{}, errors.New("host-owned admission settings are unavailable in the admitted rollout")
	}
	defer f.Close()
	// Canonical settings can precede a long turn. Unlike turn observations,
	// they must not age out of the bounded observational tail.
	for line, err := range reverseRecordPages(f, stat.Size()) {
		if err != nil {
			return Admission{}, err
		}
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

// Read newest-first in pages, stopping as soon as the owned snapshot is found.
// Memory follows one record, not the rollout size; a split record is assembled
// once. An unfinished final record is not evidence, just as in reverseRecords.
func reverseRecordPages(source io.ReaderAt, size int64) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		page := make([]byte, 64<<10)
		var fragments [][]byte
		complete := false
		for end := size; end > 0; {
			start := max(int64(0), end-int64(len(page)))
			data := page[:end-start]
			if _, err := source.ReadAt(data, start); err != nil {
				yield(nil, err)
				return
			}
			end = start
			for len(data) > 0 {
				before, part, found := bytes.CutLast(data, []byte{'\n'})
				if !complete {
					if !found {
						break
					}
					complete = true
					data = before
					continue
				}
				if !found {
					part = data
				}
				if len(fragments) > 0 {
					fragments = append(fragments, bytes.Clone(part))
					if found || start == 0 {
						slices.Reverse(fragments)
						joined := bytes.Join(fragments, nil)
						if !yield(joined, nil) {
							return
						}
						fragments = nil
					}
				} else if found || start == 0 {
					if !yield(part, nil) {
						return
					}
				} else {
					fragments = append(fragments, bytes.Clone(part))
				}
				if !found {
					break
				}
				data = before
			}
		}
	}
}

// ReadRuntime requires the exact invoking turn, even after a long turn has
// moved its context outside the observational tail. Missing or partially
// written context never substitutes a neighboring turn's authority.
// Source: codex-rs/protocol/src/protocol.rs:3426 TurnContextItem
// @0df4aa67781ae45a3c97befa4db48b734afe46d9.
func ReadRuntime(thread, path, turn string) (Runtime, error) {
	if thread == "" || turn == "" {
		return Runtime{}, errors.New("runtime snapshot requires thread and turn identity")
	}
	f, stat, ok := Open(thread, path)
	if !ok {
		return Runtime{}, errors.New("invoking turn context is unavailable in the admitted rollout")
	}
	defer f.Close()
	for line, err := range reverseRecordPages(f, stat.Size()) {
		if err != nil {
			return Runtime{}, err
		}
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
