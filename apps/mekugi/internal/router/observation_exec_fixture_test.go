package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/commandanalysis"
)

func startObservedExecTest(t *testing.T, p *observationFixture, workspace, thread, turn, call, source string) requestexecution.Prepared {
	t.Helper()
	if p.activity == nil {
		p.activity = sessioncontrol.NewAudience(nil)
	}
	if p.registry == nil {
		p.registry = sharedProxyTestRegistry(t)
	}
	if p.calls == nil {
		p.calls = testHistory().Calls()
		t.Cleanup(p.calls.Close)
	}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
		"model": "gpt-test", "tools": testExecResponsesTools(),
		"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{
			"type": "input_text", "text": "<environment_context>\n<cwd>" + workspace + "</cwd>\n<shell>bash</shell>\n</environment_context>",
		}}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := p.prepareRequest(t.Context(), &request, "preview", thread, codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: thread, TurnID: turn, Directories: map[string]json.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(prepared.Close)
	t.Cleanup(func() { p.execWindows.StopTurnPreviews(thread, turn) })
	_, err = prepared.TransformJSON(mustTestJSON(t, map[string]any{"id": "response", "status": "completed", "output": []any{map[string]any{"type": "custom_tool_call", "id": call + "-item", "call_id": call, "name": "exec", "input": source, "status": "completed"}}}))
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}
func observedCommandsSource(t *testing.T, commands []commandanalysis.Command) string {
	t.Helper()
	var source strings.Builder
	for _, command := range commands {
		fmt.Fprintf(&source, "text(await tools.exec_command(%s));", mustTestJSON(t, map[string]any{"cmd": command.Command, "workdir": command.Workdir, "shell": command.Shell, "login": false}))
	}
	return source.String()
}
