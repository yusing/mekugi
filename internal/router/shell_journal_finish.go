package router

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

const shellJournalTokenEnvironment = "MEKUGI_JOURNAL_TOKEN"

func shellJournalFinishReceipt(turn, call string) string {
	return fmt.Sprintf("shell-finish:%x", sha256.Sum256([]byte(turn+"\x00"+call)))
}

// The capability travels with the invocation, not the shared thread descriptor.
// Its question never changes while the host executes or yields that call.
func (t *mekugiResponseTransform) subscribeShellJournal(callID string, contribution toolContribution) string {
	if t.proxy.commentaryEndpoint == "" || contribution.PluginID != builtinToolsPluginID || contribution.Name != "shell" {
		return ""
	}
	broker := t.proxy.commentary
	token := broker.subscribe(t.historySessionID, callID, t.commentaryAuthor)
	if token == "" {
		return ""
	}
	broker.mu.Lock()
	route := broker.routes[token]
	route.shellCall = true
	route.originThread = t.shellThreadID
	route.journalQuestion = t.journalQuestion
	if t.shellTurnID != "" {
		route.finishReceipt = shellJournalFinishReceipt(t.shellTurnID, callID)
	}
	broker.mu.Unlock()
	t.commentarySubscriptions = append(t.commentarySubscriptions, commentarySubscription{token: token, callID: callID})
	return token
}

func (b *commentaryBroker) retireShellJournal(session, thread, call string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for token, route := range b.routes {
		if route.sessionID == session && route.originThread == thread && route.callID == call {
			b.eventCount -= len(route.events)
			delete(b.routes, token)
		}
	}
}

// Completion is proven by the enclosing host result, including a chain of host
// continuations. A later call or user input supersedes the candidate. Receipts
// survive router restart, but cannot complete another turn or a fork.
func (t *mekugiResponseTransform) shellJournalFinished(raw json.RawMessage) (bool, error) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false, nil
	}
	type invocation struct {
		executionCall
		origin string
	}
	calls := make(map[string]invocation)
	live := make(map[string]invocation)
	var candidate, active, handle, lastResult string
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
				call.nativePayload = prior.nativePayload
				delete(live, call.resumeHandle)
			}
			if history.ShellJournalTurnID != "" {
				call.origin = callID
			}
			calls[callID] = call
			if candidate != "" && handle != "" && call.resumeHandle == handle {
				active, handle, finished = callID, "", false
			} else {
				candidate, active, handle, finished = "", "", "", false
				if t.shellTurnID != "" && history.ShellJournalTurnID == t.shellTurnID {
					candidate, active = callID, callID
				}
			}
		case "custom_tool_call_output", "function_call_output":
			lastResult = callID
			call, known := calls[callID]
			delete(calls, callID)
			if !known {
				continue
			}
			next, success := shellJournalExecutionResult(call.executionCall, item["output"])
			if next != "" {
				live[next] = call
			} else if call.origin != "" {
				t.proxy.commentary.retireShellJournal(t.historySessionID, t.shellThreadID, call.origin)
			}
			if callID == active {
				handle, finished = next, success
			}
		}
	}
	if !finished || candidate == "" || lastResult != active || len(calls) != 0 || len(live) != 0 {
		return false, nil
	}
	found := false
	err := t.proxy.journals.transaction(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, func(j *threadJournal, exists bool) error {
		if exists {
			_, found = j.Receipts["runtime:"+shellJournalFinishReceipt(t.shellTurnID, candidate)]
		}
		return errJournalUnchanged
	})
	return found, err
}

// Reuse host-header and continuation parsing. Program output alone, nonzero
// exits, incomplete batches, and cancellation are never completion evidence.
func shellJournalExecutionResult(call executionCall, raw json.RawMessage) (handle string, success bool) {
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
	if !call.codeMode {
		return "", false
	}
	status, cell, body := codeModeExecutionHeader(texts[0])
	if status == "running" && (call.cellID == "" || call.cellID == cell) {
		return "cell:" + cell, false
	}
	if status != "Script completed" {
		return "", false
	}
	texts[0] = body
	var payloads []string
	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			payloads = append(payloads, text)
		}
	}
	if !call.nativePayload || len(payloads) != 1 {
		return "", false
	}
	if session := nativeJSONSession(payloads[0]); session != 0 {
		return fmt.Sprintf("session:%d", session), false
	}
	return "", shellJournalSuccessfulJSON([]byte(payloads[0]))
}

func shellJournalSuccessfulJSON(raw json.RawMessage) bool {
	var result struct {
		ExitCode *int              `json:"exit_code"`
		Session  json.RawMessage   `json:"session_id"`
		Results  []json.RawMessage `json:"results"`
		Batch    *struct {
			ProgramCount  int     `json:"program_count"`
			NotStarted    int     `json:"not_started_programs"`
			StoppedReason *string `json:"stopped_reason"`
		} `json:"batch"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return false
	}
	if result.Batch != nil {
		if result.Batch.NotStarted != 0 || result.Batch.StoppedReason != nil ||
			result.Batch.ProgramCount == 0 || result.Batch.ProgramCount != len(result.Results) {
			return false
		}
		for _, program := range result.Results {
			if !shellJournalSuccessfulJSON(program) {
				return false
			}
		}
		return true
	}
	return result.ExitCode != nil && *result.ExitCode == 0 && (len(result.Session) == 0 || string(result.Session) == "null")
}
