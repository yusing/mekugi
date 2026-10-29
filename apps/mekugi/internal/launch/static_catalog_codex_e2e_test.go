//go:build codex_e2e

package launch

import (
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
)

func TestConfiguredCatalogNativeCodex(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	env, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	var codexHome string
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			codexHome = value
		}
	}
	userPath := filepath.Join(codexHome, "user.json")
	selectedPath := filepath.Join(workspace, "selected catalog.json")
	bundled, err := bundledModelCatalog(t.Context(), codex, workspace)
	if err != nil {
		t.Fatal(err)
	}
	var catalog map[string]any
	if err := json.Unmarshal(bundled, &catalog); err != nil {
		t.Fatal(err)
	}
	model := catalog["models"].([]any)[0].(map[string]any)
	model["slug"] = "selected"
	model["supported_reasoning_levels"] = []any{map[string]any{"effort": "high", "description": "high"}, map[string]any{"effort": "max", "description": "max"}}
	for _, file := range []struct{ path, effort string }{{userPath, "max"}, {selectedPath, "high"}} {
		model["multi_agent_reasoning_effort"] = file.effort
		body, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model_catalog_json = "+strconv.Quote(userPath)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), codex, "app-server", "-c", "model_catalog_json="+strconv.Quote(selectedPath), "-c", "features.plugins=false")
	cmd.Env, cmd.Dir = env, workspace
	client, err := appserver.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id, err := client.Initialize()
	if err != nil {
		t.Fatal(err)
	}
	awaitHistoryRPC(t, client, id)
	if _, err := client.Send("initialized", map[string]any{}, false); err != nil {
		t.Fatal(err)
	}
	id, err = client.Send("config/read", map[string]any{"cwd": workspace, "includeLayers": false}, true)
	if err != nil {
		t.Fatal(err)
	}
	response := awaitHistoryRPC(t, client, id)
	driver := new(threadbinding.Driver)
	driver.CaptureConfiguredCatalog(response.Result)
	if effort, err := driver.InheritedEffort("selected", "ultra"); err != nil || effort != "high" {
		t.Fatalf("host effective config did not select CLI catalog: %q %v; response %s", effort, err, response.Result)
	}
}
