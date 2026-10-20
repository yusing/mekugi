package launch

import (
	json "encoding/json/v2"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"testing"
)

func TestWebSocketRequestPreservesRichBodyMetadata(t *testing.T) {
	headers := codexAuthHeaders()
	headers.Set(codexresponses.TurnMetadataHeader, "bounded header metadata")
	headers.Set("x-codex-turn-state", "current state")
	payload, handshake, _, err := codexresponses.WebSocketRequest([]byte(`{"model":"model","input":[],"client_metadata":{"x-codex-turn-metadata":"full body metadata","custom":"preserved"}}`), headers, "session")
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Metadata map[string]string `json:"client_metadata"`
	}
	if err := json.Unmarshal(payload, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Metadata[codexresponses.TurnMetadataHeader] != "full body metadata" || sent.Metadata["custom"] != "preserved" || sent.Metadata["x-codex-turn-state"] != "current state" || handshake.Get(codexresponses.TurnMetadataHeader) != "" || handshake.Get("x-codex-turn-state") != "" {
		t.Fatal("body metadata overwritten or per-turn metadata retained in handshake")
	}
}
