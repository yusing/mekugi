//go:build codex_e2e

package sessioncontrol

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/presets"
)

func TestPresetsNativeCodexStorage(t *testing.T) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	configRoot, workspace := t.TempDir(), t.TempDir()
	// Isolate the in-process legacy path too, so a storage regression cannot
	// read or write the developer's configuration while this test runs.
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(name, configRoot)
	}
	configPath := filepath.Join(configRoot, "config.toml")
	initial := `# Preserve unrelated settings and comments.
check_for_update_on_startup = false
[features]
plugins = false
[tui.presets.1]
input = "native prompt"
label = "Native label"
[tui.presets.9]
input = "untouched slot"
`
	initial += fmt.Sprintf("\n[projects.%q]\ntrust_level = \"untrusted\"\n", workspace)
	for _, directory := range []string{".git", ".codex"} {
		if err := os.Mkdir(filepath.Join(workspace, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Disabled raw layers may contain values invalid under the effective schema.
	if err := os.WriteFile(filepath.Join(workspace, ".codex", "config.toml"), []byte("[tui.presets.1]\ninput = 123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	legacyConfigRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	oldDirectory := filepath.Join(legacyConfigRoot, "mekugi", "presets")
	if err := os.MkdirAll(oldDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(oldDirectory, "0.json")
	oldBytes := []byte(`"unused legacy preset"`)
	if err := os.WriteFile(oldPath, oldBytes, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary, "app-server")
	command.Dir = workspace
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + configRoot, "CODEX_HOME=" + configRoot, "XDG_CONFIG_HOME=" + configRoot, "XDG_STATE_HOME=" + configRoot}
	host, err := appserver.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := host.Shutdown(); err != nil {
			t.Error(err)
		}
	}()
	id, err := host.Initialize()
	if err != nil {
		t.Fatal(err)
	}
initialize:
	for {
		select {
		case message := <-host.Messages:
			if string(message.ID) == id {
				if message.Error != nil {
					t.Fatal(message.Error.Message)
				}
				break initialize
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if _, err := host.Send("initialized", nil, false); err != nil {
		t.Fatal(err)
	}
	requests := NewRequests[struct{}](host)
	defer requests.Close()
	run := func(operation func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- operation() }()
		for {
			select {
			case call := <-requests.Calls():
				requests.Dispatch(call)
			case message := <-host.Messages:
				requests.CompleteCall(message)
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
				return
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	store := requests.Presets(ctx, "fixture", workspace)
	var slots [10]presets.Slot
	read := func() error { var err error; slots, err = store.Load(); return err }
	run(read)
	if slots[1].Input != "native prompt" || slots[1].Label != "Native label" || slots[0].Input != "" {
		t.Fatal("native slots or legacy isolation changed")
	}
	model := "/model 'gpt-example' --reasoning='high' --speed='fast'"
	run(func() error { _, err := store.Save(1, presets.Slot{Input: model, Label: "Model"}); return err })
	run(func() error { _, err := store.Save(2, presets.Slot{Input: "new prompt"}); return err })
	run(read)
	if slots[1].Input != model || slots[1].Label != "Model" || slots[2].Input != "new prompt" || slots[2].Label != "" || slots[9].Input != "untouched slot" {
		t.Fatal("slot write did not store native values and labels")
	}
	run(func() error { _, err := store.Save(2, presets.Slot{}); return err })
	run(read)
	if slots[2].Input != "" || slots[1].Input != model {
		t.Fatal("delete changed another native slot")
	}
	data, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(data), "# Preserve unrelated settings and comments.") || !strings.Contains(string(data), "plugins = false") {
		t.Fatal("unrelated native configuration changed")
	}
	data, err = os.ReadFile(oldPath)
	if err != nil || string(data) != string(oldBytes) {
		t.Fatal("legacy preset file changed")
	}
	entries, err := os.ReadDir(oldDirectory)
	if err != nil || len(entries) != 1 {
		t.Fatal("native edits mirrored into legacy storage")
	}
}
