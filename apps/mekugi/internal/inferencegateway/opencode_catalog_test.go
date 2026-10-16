package inferencegateway

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yusing/mekugi/providers/opencode"
)

func TestOpenCodeCatalogRefreshChangesInferenceFormat(t *testing.T) {
	models := map[string]opencode.Metadata{"new-model": {Format: "chat"}}
	catalog := openCodeTestCatalog(t, func() map[string]opencode.Metadata { return models })
	service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}, Catalog: catalog}).Services()[0]
	client := &ThirdPartyClient{OpenCode: &service, Diagnostics: testDiagnostics()}
	for _, format := range []string{"chat", "responses"} {
		models["new-model"] = opencode.Metadata{Format: format}
		if err := catalog.Refresh(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		client.HTTPClient = &http.Client{Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
			path, body := "/chat/completions", grokTextStream()
			if format == "responses" {
				path = "/responses"
				body = openCodeEvents(map[string]any{"type": "response.completed", "response": map[string]any{"id": "test", "status": "completed", "output": []any{}}})
			}
			if !strings.HasSuffix(request.URL.Path, path) {
				t.Fatalf("refreshed format %s sent to %s", format, request.URL)
			}
			return serverHTTPResponse(body), nil
		})}
		response, err := client.ForwardExecution(t.Context(), t.Context(), []byte(`{"model":"opencode-go:new-model","input":[]}`), grokTestHeaders())
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenCodeMetadataReasoningEffort(t *testing.T) {
	for _, format := range []string{"chat", "anthropic", "responses"} {
		t.Run(format, func(t *testing.T) {
			service := openCodeTestService(t, map[string]opencode.Metadata{"new-model": {Format: format, Efforts: []string{"none", "low", "max"}}})
			for _, effort := range []string{"", "high", "none", "low", "max"} {
				tr, err := testTranslateProviderRequest(mustTestJSON(t, map[string]any{
					"model": "opencode-go:new-model", "input": []any{},
					"reasoning": map[string]string{"effort": effort},
				}), &service)
				if err != nil {
					t.Fatalf("%s: %v", effort, err)
				}
				var got any
				switch format {
				case "chat":
					got = tr.body["reasoning_effort"]
				case "anthropic":
					if config, ok := tr.body["output_config"].(map[string]any); ok {
						got = config["effort"]
					}
				case "responses":
					if reasoning, ok := tr.body["reasoning"].(map[string]string); ok {
						got = reasoning["effort"]
					}
				}
				if effort == "" || effort == "high" {
					if got != nil {
						t.Fatalf("unsupported/inherited effort forwarded: %v", got)
					}
				} else if got != effort {
					t.Fatalf("supported effort %s became %v", effort, got)
				}
			}
		})
	}
}

func openCodeTestCatalog(t *testing.T, models func() map[string]opencode.Metadata) *opencode.Catalog {
	t.Helper()
	client := &http.Client{Transport: grokTestTransport(func(r *http.Request) (*http.Response, error) {
		entries := map[string]any{}
		var ids []map[string]string
		for id, m := range models() {
			entries[id] = map[string]any{"description": m.Description, "provider": map[string]string{"npm": map[string]string{"chat": "@ai-sdk/openai-compatible", "anthropic": "@ai-sdk/anthropic", "responses": "@ai-sdk/openai"}[m.Format]}, "limit": map[string]int{"context": m.Context}, "modalities": map[string]any{"input": m.Modalities}, "cost": m.Cost, "reasoning_options": []any{map[string]any{"type": "effort", "values": m.Efforts}}}
			ids = append(ids, map[string]string{"id": id})
		}
		body := map[string]any{"data": ids}
		if r.URL.Host == "models.opencode.ai" {
			p := map[string]any{"models": entries}
			body = map[string]any{"opencode-go": p, "opencode": p}
		}
		return serverHTTPResponse(string(mustTestJSON(t, body))), nil
	})}
	catalog := opencode.NewCatalog("", client)
	if err := catalog.Refresh(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func openCodeTestService(t *testing.T, models map[string]opencode.Metadata) opencode.Service {
	t.Helper()
	return (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}, Catalog: openCodeTestCatalog(t, func() map[string]opencode.Metadata { return models })}).Services()[0]
}
