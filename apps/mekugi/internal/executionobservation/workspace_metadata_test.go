package executionobservation

import (
	"path/filepath"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestCaptureWithoutGitWorkspaceMetadata(t *testing.T) {
	for _, admission := range []string{"unique", "unknown", "ambiguous"} {
		t.Run(admission, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			workspace := t.TempDir()
			proxy.review.IncludeThread(t.TempDir(), "other", true)
			if admission != "unknown" {
				proxy.review.IncludeThread(workspace, "thread", false)
			}
			if admission == "ambiguous" {
				proxy.review.IncludeThread(t.TempDir(), "thread", false)
			}
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{
				"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription), map[string]any{"role": "user", "content": "task"}},
				"tools": []any{map[string]any{"type": "function", "name": "lookup"}}, "tool_choice": "auto",
			}))
			if err != nil {
				t.Fatal(err)
			}
			// Codex's workspace metadata is empty outside Git repositories.
			transform, err := proxy.prepareRequest(t.Context(), &request, "session", "thread", codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: "thread"}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer transform.Close()
			transform.sessionShell = "/bin/bash"
			source := `text(await tools.mcp__threadcontrol__send_message({target:"/root",message:"progress"})); text(await tools.exec_command({cmd:"pwd; printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));`
			_, err = transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{map[string]any{
				"type": "custom_tool_call", "name": "exec", "call_id": "call", "input": source, "status": "completed",
			}}}))
			if err != nil {
				t.Fatal(err)
			}
			observation := transform.local["call"].ExecObservation
			if admission != "unique" {
				if transform.directory != "" || observation != nil {
					t.Fatal("missing workspace metadata borrowed an unproven thread scope")
				}
				return
			}
			if transform.directory != workspace || observation == nil || len(observation.Files) != 1 || observation.Files[0].Path != filepath.Join(workspace, "managed-child.txt") {
				t.Fatalf("admitted non-Git workspace lost its named before-window: cwd=%q observation=%+v", transform.directory, observation)
			}
		})
	}
}
