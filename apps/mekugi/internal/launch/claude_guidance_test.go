package launch

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudeGuidanceAndPreload(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(parent, "AGENTS.md"):         "parent rules",
		filepath.Join(child, "AGENTS.md"):          "superseded rules",
		filepath.Join(child, "AGENTS.override.md"): "child rules",
		filepath.Join(child, "source.txt"):         "first\nsecond\nthird\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	prompt, err := claudeSystemPrompt(control.Settings{Workspace: child, DeveloperInstructions: new("frozen role")}, control.Identity{Thread: "native-id"}, "private tools")
	var value map[string]string
	if err != nil || json.Unmarshal(prompt, &value) != nil {
		t.Fatalf("prompt: %s, %v", prompt, err)
	}
	if value["preset"] != "claude_code" || !strings.Contains(value["append"], "frozen role") || !strings.Contains(value["append"], "private tools") || strings.Contains(value["append"], "superseded rules") || strings.Index(value["append"], "parent rules") >= strings.Index(value["append"], "child rules") {
		t.Fatalf("native guidance lost captured or ordered instructions: %s", prompt)
	}
	body, err := claudePreload(t.Context(), `{"sources":[{"path":"source.txt","ranges":[[2,2]]},{"path":"missing.txt"},{"path":"AGENTS.md"}]}`, child, os.Environ())
	if err != nil || !strings.Contains(body, "second\n") || strings.Contains(body, "first\n") || !strings.Contains(body, "missing.txt") || !strings.Contains(body, "exit_code=1") || strings.Contains(body, "superseded rules") {
		t.Fatalf("preload lost cwd, ranges or failures: %q, %v", body, err)
	}
}
