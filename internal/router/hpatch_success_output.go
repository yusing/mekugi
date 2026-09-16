package router

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// Successful execution is still reported in full by the host carrier. Only the
// model-facing view is compacted; a durable digest recognizes that exact view
// on replay without adding receipt text or reviving private continuation state.
func (t *mekugiResponseTransform) projectHpatchSuccess(callID string, raw json.RawMessage, recovery hpatchRecovery) (json.RawMessage, bool) {
	if t.proxy.replayStore == nil {
		return raw, false
	}
	compact := compactHpatchSuccess(raw, recovery.Handle)
	candidate := compact
	if candidate == nil {
		candidate = raw
	}
	var value any
	if json.Unmarshal(candidate, &value) != nil {
		return raw, false
	}
	digest := sha256.Sum256(mustMarshalJSON(value))
	encoded := hex.EncodeToString(digest[:])
	store := t.proxy.replayStore.scoped(t.ctx)
	matched := false
	err := store.locked(t.ctx, func() error {
		record, exists, err := store.read(t.directory, callID, false)
		if err != nil || !exists {
			return err
		}
		owner := hpatchRecoveryFor(record.History)
		if owner == nil || owner.Handle != recovery.Handle {
			return nil
		}
		if compact == nil {
			matched = record.HpatchSuccessDigest == encoded
			return nil
		}
		record.HpatchSuccessDigest = encoded
		if err := store.write(record); err != nil {
			return err
		}
		matched = true
		return nil
	})
	// Compaction is auxiliary. If its receipt cannot be persisted, preserve the
	// full host result, which already contains all success/recovery evidence.
	if err != nil || !matched {
		return raw, false
	}
	return candidate, true
}

func validHpatchSuccessDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func compactHpatchSuccess(raw json.RawMessage, handle string) json.RawMessage {
	var parts []json.RawMessage
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parts = []json.RawMessage{hpatchOutputText(text)}
	} else if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil
	}
	var first struct{ Type, Text string }
	if json.Unmarshal(parts[0], &first) != nil || first.Type != "input_text" {
		return nil
	}
	status, _, body := codeModeExecutionHeader(first.Text)
	if status != "Script completed" {
		return nil
	}
	header := strings.TrimSuffix(first.Text, body)
	if body != "" {
		parts = append([]json.RawMessage{hpatchOutputText(header), hpatchOutputText(body)}, parts[1:]...)
	}
	found := -1
	var replacement []json.RawMessage
	for index := 1; index < len(parts); index++ {
		var part struct{ Type, Text string }
		if json.Unmarshal(parts[index], &part) != nil || part.Type != "input_text" {
			continue
		}
		if outputs := hpatchSuccessParts(part.Text, handle); outputs != nil {
			if found >= 0 {
				return nil
			}
			found, replacement = index, outputs
		}
	}
	if found < 0 {
		return nil
	}
	compact := slices.Clone(parts[:found])
	compact = append(compact, replacement...)
	compact = append(compact, parts[found+1:]...)
	return mustMarshalJSON(compact)
}

func hpatchOutputText(text string) json.RawMessage {
	return mustMarshalJSON(map[string]string{"type": "input_text", "text": text})
}

func hpatchSuccessParts(text, handle string) []json.RawMessage {
	var result struct {
		Handle            string                       `json:"resume_handle"`
		Results           []map[string]json.RawMessage `json:"results"`
		CleanupDiagnostic json.RawMessage              `json:"cleanup_diagnostic"`
		Sequence          *struct {
			Count      *int            `json:"segment_count"`
			Started    *int            `json:"started_segments"`
			NotStarted *int            `json:"not_started_segments"`
			Stopped    json.RawMessage `json:"stopped_reason"`
		} `json:"sequence"`
	}
	if json.Unmarshal([]byte(text), &result) != nil || result.Handle != handle ||
		result.Sequence == nil || result.Sequence.Count == nil || result.Sequence.Started == nil || result.Sequence.NotStarted == nil ||
		*result.Sequence.Count <= 0 || *result.Sequence.Count != len(result.Results) || *result.Sequence.Started != len(result.Results) ||
		*result.Sequence.NotStarted != 0 || string(result.Sequence.Stopped) != "null" ||
		result.CleanupDiagnostic != nil {
		return nil
	}
	outputs := make([]json.RawMessage, 0, len(result.Results))
	for index, segment := range result.Results {
		var position int
		if json.Unmarshal(segment["segment"], &position) != nil || position != index+1 ||
			jsonString(segment, "status") != "completed" {
			return nil
		}
		switch jsonString(segment, "kind") {
		case "edit":
			var report string
			if json.Unmarshal(segment["report"], &report) != nil || report == "" {
				return nil
			}
			outputs = append(outputs, hpatchOutputText(report))
		case "shell":
			var output string
			var exit int
			if json.Unmarshal(segment["output"], &output) != nil ||
				json.Unmarshal(segment["exit_code"], &exit) != nil || string(segment["exit_code"]) == "null" || exit != 0 ||
				(segment["session_id"] != nil && string(segment["session_id"]) != "null") {
				return nil
			}
			for _, field := range []string{"segment", "line", "kind", "status", "phase", "repair"} {
				delete(segment, field)
			}
			outputs = append(outputs, hpatchOutputText(string(mustMarshalJSON(segment))))
		default:
			return nil
		}
	}
	return outputs
}
