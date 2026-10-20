package launch

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func writeStockRollout(t *testing.T, path string, records ...map[string]any) {
	t.Helper()
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stockRolloutItem(payload any) map[string]any {
	return map[string]any{"timestamp": "2026-09-15T10:00:00Z", "type": "response_item", "payload": payload}
}
