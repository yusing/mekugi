package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
)

// filterCodeMode accepts the complete result of one literal exec_command,
// proven by toolActivityUnwrapExec(..., true), including its metadata-only
// retained:false copy form. Arbitrary printed
// JavaScript, batches, partial cells, and continuation results are not mapped
// back to commands. This projects text only; Codex remains the executor.
func (f *exploreFilter) filterCodeMode(ctx context.Context, task exploreTask, raw []byte, store *mekugiReplayStore) []byte {
	var text string
	var parts []map[string]jsontext.Value
	stringOutput := json.Unmarshal(raw, &text) == nil
	if !stringOutput {
		if json.Unmarshal(raw, &parts) != nil || len(parts) != 2 {
			return nil
		}
		for _, part := range parts {
			var kind string
			if json.Unmarshal(part["type"], &kind) != nil || kind != "input_text" {
				return nil
			}
		}
		if json.Unmarshal(parts[0]["text"], &text) != nil {
			return nil
		}
	}
	status, _, body := codeModeExecutionHeader(text)
	if status != "Script completed" {
		return nil
	}
	header := text[:len(text)-len(body)]
	if !stringOutput {
		if body != "" || json.Unmarshal(parts[1]["text"], &body) != nil {
			return nil
		}
	}
	var result map[string]jsontext.Value
	if json.Unmarshal([]byte(body), &result) != nil {
		return nil
	}
	var exit *int
	var session *int64
	var stdout string
	if json.Unmarshal(result["exit_code"], &exit) != nil || exit == nil ||
		json.Unmarshal(result["output"], &stdout) != nil {
		return nil
	}
	if value, exists := result["session_id"]; exists && (json.Unmarshal(value, &session) != nil || session != nil) {
		return nil
	}
	// Adapt only the result body to the shared filter. The synthetic header is
	// never exposed and the original host metadata remains in the envelope.
	nativeHeader := fmt.Sprintf("Wall time: 0 seconds\nProcess exited with code %d\nOutput:\n", *exit)
	filtered, ok := f.filter(ctx, task, nativeHeader+stdout, store)
	if !ok {
		return nil
	}
	result["output"], _ = json.Marshal(filtered[len(nativeHeader):])
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	if stringOutput {
		encoded, _ = json.Marshal(header + string(encoded))
	} else {
		parts[1]["text"], _ = json.Marshal(string(encoded))
		encoded, _ = json.Marshal(parts)
	}
	return encoded
}
