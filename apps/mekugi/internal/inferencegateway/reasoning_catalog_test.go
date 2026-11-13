package inferencegateway

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestAuxiliaryReasoningUsesSelectedForwardedCatalog(t *testing.T) {
	const body = `{"models":[{"slug":"selected","default_reasoning_level":"high","multi_agent_reasoning_effort":"medium","supports_reasoning_effort_updates":true,"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"ultra"}]}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer upstream.Close()
	var catalog ReasoningCatalog
	handler := ModelsHandler(newTestClient(upstream.URL, upstream.Client()), nil, catalog.Capture)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header = hostHTTPHeaders()
	output := httptest.NewRecorder()
	handler(output, request)
	if output.Code != http.StatusOK || output.Body.String() != body {
		t.Fatalf("catalog forwarding changed: %d %s", output.Code, output.Body.String())
	}
	fallback, choices, updates := catalog.Choices("selected")
	if fallback != "high" || !slices.Equal(choices, []string{"low", "medium", "high", "ultra"}) || !updates {
		t.Fatalf("selected reasoning=%q %v %v", fallback, choices, updates)
	}
	choices[0] = "mutated"
	_, fresh, _ := catalog.Choices("selected")
	if fresh[0] != "low" {
		t.Fatal("borrower mutated selected catalog")
	}
	if effort, err := catalog.InheritedEffort("selected", "ultra"); err != nil || effort != "medium" {
		t.Fatalf("inherited effort=%q err=%v", effort, err)
	}
	if fallback, choices, updates := catalog.Choices("unknown"); fallback != "" || len(choices) != 0 || updates {
		t.Fatal("unknown model inherited another model's policy")
	}
}

func TestConfiguredReasoningCatalogRefreshAndFailure(t *testing.T) {
	var catalog ReasoningCatalog
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(`{"models":[{"slug":"configured","supported_reasoning_levels":[{"effort":"low"},{"effort":"max"},{"effort":"ultra"}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"config": map[string]string{"model_catalog_json": path}})
	if err != nil {
		t.Fatal(err)
	}
	catalog.CaptureConfigured(raw)
	if effort, err := catalog.InheritedEffort("configured", "ultra"); err != nil || effort != "max" {
		t.Fatalf("configured effort=%q err=%v", effort, err)
	}
	catalog.Capture([]byte(`{"models":[{"slug":"replacement","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`))
	if _, err := catalog.InheritedEffort("configured", "ultra"); err == nil {
		t.Fatal("refresh retained stale configured model")
	}
	if effort, err := catalog.InheritedEffort("replacement", "ultra"); err != nil || effort != "high" {
		t.Fatalf("replacement effort=%q err=%v", effort, err)
	}
	catalog.Capture([]byte(`malformed`))
	if _, err := catalog.InheritedEffort("replacement", "ultra"); err == nil {
		t.Fatal("failed acquisition retained stale policy")
	}
	if fallback, choices, updates := catalog.Choices("replacement"); fallback != "" || len(choices) != 0 || updates {
		t.Fatal("failed acquisition retained auxiliary policy")
	}
	custom := ReasoningCatalog{NativeModels: true}
	if effort, err := custom.InheritedEffort("custom", "ultra"); err != nil || effort != "ultra" {
		t.Fatalf("custom provider policy changed: %q %v", effort, err)
	}
}
