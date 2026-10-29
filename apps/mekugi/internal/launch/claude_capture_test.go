package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func TestClaudeLaunchNativeCaptureWorkingDirectories(t *testing.T) {
	for _, scenario := range []struct {
		tool             string
		removedWorkspace bool
	}{{tool: "Bash"}, {tool: "PowerShell"}, {tool: "Bash", removedWorkspace: true}} {
		name := scenario.tool
		if scenario.removedWorkspace {
			name += "/removed-workspace"
		}
		t.Run(name, func(t *testing.T) {
			f := newClaudeHookFixture(t)
			b := f.bridge
			workspace := b.settings.Workspace
			if scenario.removedWorkspace {
				if err := os.Remove(workspace); err != nil {
					t.Fatal(err)
				}
			}
			// Sibling native cwd values must not change the retained workspace or
			// cause one terminal hook to claim the other's explicitly named file.
			var directories [2]string
			var arguments [2]jsontext.Value
			for i := range directories {
				directories[i] = t.TempDir()
				if scenario.tool == "PowerShell" {
					// Its interpreter is opaque to command analysis. Existing bounded
					// workspace comparison, not Bash operand parsing, supplies evidence.
					directories[i] = filepath.Join(workspace, []string{"left", "right"}[i])
					if err := os.Mkdir(directories[i], 0700); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(directories[i], "target.ipynb")
				if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
					t.Fatal(err)
				}
				args := map[string]string{"command": "printf 'after\\n' > target.ipynb"}
				if scenario.tool == "PowerShell" {
					args = map[string]string{"command": "Set-Content target.ipynb 'after'"}
				}
				var err error
				arguments[i], err = json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
			}
			hook := func(i int, event string) jsontext.Value {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"session_id": b.info.Session, "cwd": directories[i], "hook_event_name": event, "tool_name": scenario.tool, "tool_use_id": directories[i], "tool_input": arguments[i], "tool_response": map[string]bool{"success": true}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := b.request(t.Context(), "hook", raw)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			for i := range directories {
				pre := hook(i, "PreToolUse")
				if strings.Contains(string(pre), `"permissionDecision":"deny"`) || b.captures[directories[i]] == nil || b.captures[directories[i]].operation == nil {
					t.Fatalf("native capture not admitted: %s", pre)
				}
			}
			for i := range directories {
				if scenario.tool == "Bash" {
					command := exec.CommandContext(t.Context(), execTrackShellExecutable(t, "bash"), "-c", "printf 'after\\n' > target.ipynb")
					command.Dir = directories[i]
					if output, err := command.CombinedOutput(); err != nil {
						t.Fatalf("native shell: %s, %v", output, err)
					}
				} else if err := os.WriteFile(filepath.Join(directories[i], "target.ipynb"), []byte("after\n"), 0600); err != nil {
					t.Fatal(err)
				}
				result := hook(i, "PostToolUse")
				var response struct {
					Specific struct {
						Context string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(result, &response); err != nil {
					t.Fatal(err)
				}
				fields := strings.Fields(response.Specific.Context)
				if len(fields) < 3 || fields[0] != "mchanges" {
					t.Fatalf("missing retained receipt: %s", result)
				}
				parent := b.selected
				parent.Thread = "parent"
				diff, err := b.owners.Changes.Service.Read(t.Context(), parent, changes.Query{Workspace: workspace, IDs: []string{fields[1]}})
				path := filepath.Join(directories[i], "target.ipynb")
				if scenario.tool == "PowerShell" {
					path, _ = filepath.Rel(workspace, path)
				}
				if err != nil || !strings.Contains(diff, path) || !strings.Contains(diff, "-before") || !strings.Contains(diff, "+after") || strings.Contains(diff, filepath.Join(directories[1-i], "target.ipynb")) {
					t.Fatalf("native cwd effect or retained workspace lost: %s, %v", diff, err)
				}
				own, err := b.owners.Changes.Service.Read(t.Context(), parent, changes.Query{Workspace: workspace, View: "list"})
				if err != nil || strings.Contains(own, fields[1]) {
					t.Fatalf("child attribution leaked into parent implicit selection: %q, %v", own, err)
				}
			}
		})
	}
}
