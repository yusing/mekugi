package router

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/yusing/mekugi/capturer"
)

func journalHostFinishReceipt(turn, call string) string {
	return fmt.Sprintf("host-finish:%x", sha256.Sum256([]byte(turn+"\x00"+call)))
}

// Finish is invocation control, not a journal node or a replayed mutation.
func splitJournalFinish(mutations []journalMutation) ([]journalMutation, bool, error) {
	for i, mutation := range mutations {
		if mutation.Op != "finish" {
			continue
		}
		if i != len(mutations)-1 || mutation.Agent != "" || mutation.P != "" || mutation.Under != "" || mutation.Kind != "" ||
			mutation.Title != nil || mutation.Body != nil || mutation.State != nil || mutation.Reason != nil || mutation.SupersededBy != nil || mutation.Before != "" ||
			mutation.Reset != "" || mutation.Tasks != nil || mutation.ID != "" || mutation.Text != nil || mutation.Answer != nil || mutation.ReportNow {
			return nil, false, errors.New("journal finish accepts only op and must be the last operation")
		}
		return mutations[:i], true, nil
	}
	return mutations, false, nil
}

// Source: internal/router/shell_journal_finish.go@518ac19ac89d17d4ef8b038422b46b124811dc17 shellJournalFinished.
// Keep v1's invocation and continuation scope, using current stock host evidence.
func (t *mekugiResponseTransform) journalHostFinished(raw jsonv1.RawMessage) (bool, error) {
	var items []map[string]jsonv1.RawMessage
	if t.shellTurnID == "" || json.Unmarshal(raw, &items) != nil {
		return false, nil
	}
	var receipts map[string]journalReceipt
	err := t.proxy.journals.transaction(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, func(j *threadJournal, exists bool) error {
		if exists {
			receipts = j.Receipts
		}
		return errJournalUnchanged
	})
	if err != nil {
		return false, err
	}
	type invocation struct {
		executionCall
		origin string
	}
	calls := make(map[string]invocation)
	live := make(map[string]invocation)
	var candidate, active, handle, lastResult, candidateSource string
	var candidateCodeMode bool
	finished := false
	for _, item := range items {
		if jsonString(item, "role") == "user" {
			candidate, active, handle, finished = "", "", "", false
		}
		callID := jsonString(item, "call_id")
		if callID == "" {
			continue
		}
		switch jsonString(item, "type") {
		case "custom_tool_call", "function_call":
			history, known := t.visible[callID]
			call := invocation{executionCall: executionCallFor(item, history, known, t.codeModeToolName)}
			if prior, ok := live[call.resumeHandle]; ok {
				call.origin = prior.origin
				delete(live, call.resumeHandle)
			}
			_, legacyFinish := receipts["runtime:"+journalHostFinishReceipt(t.shellTurnID, callID)]
			_, mcpFinish := receipts["runtime:"+journalHostFinishReceipt(t.shellTurnID, jsonString(item, "id"))]
			if legacyFinish && history.JournalFinishTurnID == t.shellTurnID && history.ExecutingThread == t.shellThreadID || mcpFinish {
				call.origin = callID
			}
			calls[callID] = call
			if candidate != "" && handle != "" && call.resumeHandle == handle {
				active, handle, finished = callID, "", false
			} else {
				candidate, active, handle, finished = "", "", "", false
				if call.origin == callID {
					candidate, active = callID, callID
					candidateCodeMode = call.codeMode
					candidateSource = jsonString(item, "input")
					if known {
						candidateSource = history.carrierInput()
					}
				}
			}
		case "custom_tool_call_output", "function_call_output":
			lastResult = callID
			call, known := calls[callID]
			delete(calls, callID)
			if !known {
				continue
			}
			next, success := journalHostExecutionResult(call.executionCall, item["output"])
			if next != "" {
				live[next] = call
			}
			if callID == active {
				handle, finished = next, success
			}
		}
	}
	if !finished || candidate == "" || lastResult != active || len(calls) != 0 || len(live) != 0 {
		return false, nil
	}
	if candidateCodeMode {
		cell := t.proxy.nativeTrace.readCell(t.shellThreadID, candidate, candidateSource)
		if cell == nil || cell.pending() {
			return false, nil
		}
		for _, tool := range cell.tools {
			if !tool.terminal || tool.Status != "completed" || tool.ExitCode != nil && *tool.ExitCode != 0 {
				return false, nil
			}
		}
	}
	return true, nil
}

func journalHostExecutionResult(call executionCall, raw jsonv1.RawMessage) (handle string, success bool) {
	texts := executionOutputTexts(raw)
	if len(texts) == 0 {
		return "", false
	}
	if call.native {
		if session := nativeExecutionSession(texts[0]); session != 0 {
			return fmt.Sprintf("session:%d", session), false
		}
		state, _ := nativeExecutionHeader(texts[0])
		return "", state == "Process exited with code 0"
	}
	if call.codeMode {
		status, cell, _ := codeModeExecutionHeader(texts[0])
		if status == "running" && (call.cellID == "" || call.cellID == cell) {
			return "cell:" + cell, false
		}
		return "", status == "Script completed"
	}
	return "", false
}

// Source: internal/router/server.go@518ac19ac89d17d4ef8b038422b46b124811dc17 journalFinishResponseProvider.
// Codex still owns its tool-result continuation; only provider inference is skipped.
func (a *requestAttempt) tryJournalHostFinish() (bool, error) {
	t := a.mekugiTransform
	if t == nil || a.prewarm {
		return false, nil
	}
	finished, err := t.journalHostFinished(a.request.fields["input"])
	if err != nil || !finished {
		return false, err
	}
	capturer.ObserveNoProvider(a.startCtx)
	t.journalFinishRequested = true
	// This response did not admit inference. Terminal journal delivery must
	// not finish the prepared observation as a missing provider-usage report.
	t.usageTracker = nil
	id := "resp_mekugi_journal_" + rand.Text()
	response := map[string]any{"id": id, "object": "response", "status": "completed", "output": []any{}}
	body := mustMarshalJSON(response)
	contentType := "application/json"
	if a.request.streamResponse {
		created := mustMarshalJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}})
		completed := mustMarshalJSON(map[string]any{"type": "response.completed", "response": response})
		body = []byte("data: " + string(created) + "\n\ndata: " + string(completed) + "\n\n")
		contentType = "text/event-stream"
	}
	a.response = &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(bytes.NewReader(body))}
	a.streamResponse = a.request.streamResponse
	a.releaseDelivery = true
	a.finalization.upstreamStatusCode = http.StatusOK
	t.featureTrace.record("journal", "host_result", "completion", "local", "", id)
	return true, nil
}
