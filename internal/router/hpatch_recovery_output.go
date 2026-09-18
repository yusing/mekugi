package router

import (
	"encoding/json"
	"io"
	"strings"
	"time"
)

// Recovery is projected only when the host returns without a complete carrier
// result. It describes retained facts, not liveness or permission to retry.
type hpatchRecovery struct {
	Handle            string                 `json:"resume_handle"`
	ExpiresAt         time.Time              `json:"expires_at"`
	Availability      string                 `json:"availability,omitempty"`
	CompletedSegments *int                   `json:"completed_segments,omitempty"`
	Current           *hpatchRecoverySegment `json:"current,omitempty"`
	ControlSessionID  int64                  `json:"control_session_id,omitempty"`
}

type hpatchRecoverySegment struct {
	Segment   int    `json:"segment"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	SessionID int64  `json:"session_id,omitempty"`
	Repair    bool   `json:"repair,omitempty"`
}

func hpatchRecoveryFor(history mekugiHistory) *hpatchRecovery {
	if (history.ToolName != mekugiToolName && history.ToolName != mekugiRecoveryToolName) || history.TranslationError != "" || history.ReplayCarrier {
		return nil
	}
	config, ok := strings.CutPrefix(history.CarrierPayload, "const mixedConfig = ")
	if !ok {
		return nil
	}
	config, _, ok = strings.Cut(config, ";\n")
	var decoded struct {
		State struct {
			Handle    string    `json:"handle"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"state"`
	}
	if !ok || json.Unmarshal([]byte(config), &decoded) != nil || decoded.State.ExpiresAt.IsZero() {
		return nil
	}
	if _, err := mixedArtifactName(decoded.State.Handle); err != nil {
		return nil
	}
	return &hpatchRecovery{Handle: decoded.State.Handle, ExpiresAt: decoded.State.ExpiresAt}
}

func hasHpatchFinalResult(texts []string, handle string) bool {
	for index, text := range texts {
		if index == 0 {
			if status, _, body := codeModeExecutionHeader(text); status != "" {
				text = body
			}
		}
		var result struct {
			Handle   string            `json:"resume_handle"`
			Results  []json.RawMessage `json:"results"`
			Sequence *struct {
				Count      int             `json:"segment_count"`
				Started    int             `json:"started_segments"`
				NotStarted int             `json:"not_started_segments"`
				Stopped    json.RawMessage `json:"stopped_reason"`
			} `json:"sequence"`
		}
		if json.Unmarshal([]byte(text), &result) == nil && result.Handle == handle &&
			result.Results != nil && result.Sequence != nil && result.Sequence.Stopped != nil &&
			result.Sequence.Started == len(result.Results) && result.Sequence.Count > 0 &&
			result.Sequence.NotStarted >= 0 &&
			result.Sequence.Count == result.Sequence.Started+result.Sequence.NotStarted {
			return true
		}
	}
	return false
}

func (t *mekugiResponseTransform) readHpatchRecovery(recovery hpatchRecovery) hpatchRecovery {
	recovery.Availability = "unavailable; retained state is not evidence of process termination"
	if !time.Now().Before(recovery.ExpiresAt) {
		recovery.Availability = "expired; do not replay the script or infer rollback"
		return recovery
	}
	name, err := mixedArtifactName(recovery.Handle)
	if err != nil {
		return recovery
	}
	root, release, err := t.proxy.shellRoot(t.shellDirectory)
	if err != nil {
		return recovery
	}
	defer release()
	file, err := openRegularShellFile(root, name)
	if err != nil {
		return recovery
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxHpatchCheckpointBytes+1))
	var state hpatchResumeState
	if err != nil || len(data) > maxHpatchCheckpointBytes || json.Unmarshal(data, &state) != nil ||
		state.Handle != recovery.Handle || state.Root != t.directory ||
		!state.ExpiresAt.Equal(recovery.ExpiresAt) {
		return recovery
	}
	var results []json.RawMessage
	var current *hpatchRecoverySegment
	var control int64
	if json.Unmarshal(state.Progress["results"], &results) != nil || results == nil {
		return recovery
	}
	if raw := state.Progress["current"]; raw != nil && json.Unmarshal(raw, &current) != nil {
		return recovery
	}
	if current != nil && (current.Segment <= 0 || current.Line <= 0 ||
		(current.Kind != "edit" && current.Kind != "shell") || current.Phase == "" || current.Status == "") {
		return recovery
	}
	if raw := state.Progress["control_session_id"]; raw != nil && json.Unmarshal(raw, &control) != nil {
		return recovery
	}
	recovery.Availability = "retained snapshot; resolve host activity before resuming"
	completed := len(results)
	if current != nil && current.Status == "completed" {
		completed++
	}
	recovery.CompletedSegments = &completed
	recovery.Current, recovery.ControlSessionID = current, control
	return recovery
}
