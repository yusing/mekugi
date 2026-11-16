//go:build codex_e2e

package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"html"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func environmentFacts(t *testing.T, raw []byte) string {
	t.Helper()
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	var facts string
	for _, item := range items {
		content := responses.StringField(item, "content")
		if content == "" {
			var parts []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(item["content"], &parts); err != nil {
				continue
			}
			for _, part := range parts {
				content += part.Text
			}
		}
		if !strings.Contains(content, "<environment_context>") {
			continue
		}
		_, body, found := strings.Cut(content, "Observed at: ")
		if !found {
			continue
		}
		body, _, found = strings.Cut(body, "</environment_context>")
		if !found {
			t.Fatal("incomplete native environment context")
		}
		facts = html.UnescapeString("Observed at: " + strings.TrimSpace(body))
	}
	if facts == "" {
		t.Fatal("native environment context missing captured facts")
	}
	return facts
}
