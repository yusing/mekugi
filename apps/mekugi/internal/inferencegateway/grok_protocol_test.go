package inferencegateway

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGrokCodeModeExecNamesPrintedResults(t *testing.T) {
	var request map[string]any
	_ = json.Unmarshal(grokTestRequest(t, true), &request)
	request["tools"] = append(request["tools"].([]any), map[string]any{"type": "custom", "name": "other", "description": "Other input"})
	tr, err := testTranslateProviderRequest(mustTestJSON(t, request), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := make(map[string]string)
	for _, tool := range tr.body["tools"].([]any) {
		fn := tool.(map[string]any)["function"].(map[string]any)
		descriptions[fn["name"].(string)] = fn["description"].(string)
	}
	if got := descriptions["exec"]; !strings.HasPrefix(got, "Run exact input\n") || !strings.Contains(got, "text(...)") || !strings.Contains(got, "JavaScript") {
		t.Fatalf("exec description = %q", got)
	}
	if got := descriptions["other"]; got != "Other input" {
		t.Fatalf("other custom tool description = %q", got)
	}
}
