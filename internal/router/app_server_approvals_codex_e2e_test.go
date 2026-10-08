//go:build journal_e2e

package router

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func TestNativeApprovalDenialSameTurnCodexE2E(t *testing.T) {
	const reason = "Use the existing result instead."
	shell := execTrackShellExecutable(t, "bash")
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	var original codexTurnMetadata
	requests := 0
	provider := &toolFrontendCodexProvider{
		program:   `const result = await tools.exec_command({cmd:"echo APPROVAL_COMMAND_RAN",shell:` + string(mustMarshalJSON(shell)) + `,login:false,sandbox_permissions:"require_escalated",justification:"Exercise isolated native command denial"}); text(result.output);`,
		expected:  []string{"rejected"},
		finalText: "Recovered after a retry.",
		observeRequest: func(body []byte, headers http.Header) error {
			requests++
			metadata, ok := decodeCodexTurnMetadata(headers)
			if !ok || metadata.ThreadID == "" || metadata.TurnID == "" {
				return fmt.Errorf("missing native thread/turn identity")
			}
			if requests == 1 {
				original = metadata
				return nil
			}
			if requests != 2 || metadata.ThreadID != original.ThreadID || metadata.TurnID != original.TurnID {
				return fmt.Errorf("denial continuation changed turn or added request: requests=%d original=%s/%s current=%s/%s", requests, original.ThreadID, original.TurnID, metadata.ThreadID, metadata.TurnID)
			}
			var request struct {
				Input []struct {
					Type    string         `json:"type"`
					Role    string         `json:"role"`
					CallID  string         `json:"call_id"`
					Content jsontext.Value `json:"content"`
					Output  jsontext.Value `json:"output"`
				} `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				return err
			}
			denied, feedback := false, 0
			for _, item := range request.Input {
				if item.Type == "custom_tool_call_output" && item.CallID == "frontend-call" {
					denied = strings.Contains(string(item.Output), "rejected") && !strings.Contains(string(item.Output), "APPROVAL_COMMAND_RAN")
				}
				if item.Role == "user" && strings.Contains(string(item.Content), "user denied this command with a reason: "+reason) {
					feedback++
				}
			}
			if !denied || feedback != 1 {
				return fmt.Errorf("first same-turn follow-up lacks atomic denial/reason: denied=%t feedback=%d body=%.3000s", denied, feedback, body)
			}
			return nil
		},
	}
	runAppServerPreviewWith(t, provider, proxy, appServerPreview{
		environment: []string{"PATH=" + execTrackPath(), vcsguard.HookEnvironment + "="},
		codexArgs:   []string{"-c", `sandbox_mode="read-only"`, "-c", `approval_policy="on-request"`, "-c", "hooks={}"},
		approvals:   true,
		noJournal:   true,
		duringTurn: func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
			await("Run this command?")
			if _, err := io.WriteString(outer, reason+"\r"); err != nil {
				t.Fatal(err)
			}
		},
	})
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.turns != 2 || !provider.resultSeen {
		t.Fatalf("native denial acceptance: requests=%d result=%t output=%s", provider.turns, provider.resultSeen, provider.output)
	}
}
