package launch

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/launch/modelcatalog"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/providers/opencode"
)

// Exercise the provider's public refresh boundary with provider-shaped records.
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

func TestOpenCodeCatalogConsumers(t *testing.T) {
	models := map[string]opencode.Metadata{"new-model": {Format: "chat", Description: "Upstream description", Efforts: []string{"max"}, Cost: opencode.Price{Input: new(1.0), Output: new(5.0), CacheRead: new(0.0), CacheWrite: new(2.0)}}}
	catalog := openCodeTestCatalog(t, func() map[string]opencode.Metadata { return models })
	config := opencode.Config{Go: opencode.ServiceConfig{APIKey: "private-key"}, Catalog: catalog}
	service := config.Services()[0]
	body, err := modelcatalog.Build([]byte(`{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2"}]}`), modelcatalog.Selection{OpenCode: config})
	if err != nil || !strings.Contains(string(body), `"description":"Upstream description"`) || !strings.Contains(string(body), `"effort":"max"`) || !strings.Contains(string(body), `"default_reasoning_level":null`) {
		t.Fatalf("catalog projection: %s %v", body, err)
	}
	m, _ := service.Model("new-model")
	cost := requestexecution.EstimateOpenCodeCost(m.Cost, "", inference.Usage{InputTokens: 100, UncachedInputTokens: 80, CacheWriteTokens: 10, OutputTokens: 20})
	if !cost.Known || math.Abs(cost.UncachedInput-90.0/1e6) > 1e-12 || cost.CachedInput != 0 || cost.Output != 100.0/1e6 {
		t.Fatalf("online pricing: %+v", cost)
	}
	free := opencode.Price{Input: new(0.0), Output: new(0.0)}
	counts := inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}
	if !requestexecution.EstimateOpenCodeCost(free, "", counts).Known {
		t.Fatal("explicit zero price is not free")
	}
	counts.UncachedInputTokens = 5
	if requestexecution.EstimateOpenCodeCost(free, "", counts).Known || requestexecution.EstimateOpenCodeCost(opencode.Price{}, "", counts).Known || requestexecution.EstimateOpenCodeCost(free, "priority", inference.Usage{}).Known {
		t.Fatal("missing rates or unsupported tier priced")
	}
}

func TestOpenCodeInflightPriceSurvivesRemoval(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			models := map[string]opencode.Metadata{"glm-5.3-flash": {Format: "chat", Cost: opencode.Price{Input: new(1.0), Output: new(2.0)}}}
			catalog := openCodeTestCatalog(t, func() map[string]opencode.Metadata { return models })
			service := (opencode.Config{Go: opencode.ServiceConfig{APIKey: "test"}, Catalog: catalog}).Services()[0]
			client := &inferencegateway.ThirdPartyClient{Diagnostics: requestexecution.ProviderDiagnostics(), OpenCode: &service, HTTPClient: &http.Client{
				Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
					// Another request refreshes/removes the model after this
					// request selected its format and prices, before completion.
					models = map[string]opencode.Metadata{"replacement": {Format: "chat"}}
					if err := catalog.Refresh(t.Context(), true); err != nil {
						t.Fatal(err)
					}
					return serverHTTPResponse(grokTestSSE(
						map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "OK"}, "finish_reason": "stop"}}},
						map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4}},
					)), nil
				}),
			}}
			response, err := client.ForwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, stream), grokTestHeaders())
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			usage := newTestUsage(t)
			observation := usage.Observation("thread", "", "opencode-go:glm-5.3-flash", "")
			observation.Price = requestexecution.PinnedOpenCodePrice(response.Price)
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatal(err)
			}
			observation.Observe(inference.Usage{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 4})
			report, ok := usage.Snapshot("thread")
			if !ok || !report.Cost.Known || report.Cost.UncachedInput != 10.0/1e6 || report.Cost.Output != 8.0/1e6 {
				t.Fatalf("in-flight price lost after removal: %+v", report)
			}
		})
	}
}
