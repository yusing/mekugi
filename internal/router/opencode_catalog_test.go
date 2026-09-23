package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func storeOpenCodeTestSnapshot(catalog *openCodeCatalog, mutate func(*openCodeSnapshot)) *openCodeSnapshot {
	current := catalog.current()
	next := &openCodeSnapshot{Version: current.Version, Updated: current.Updated, Models: maps.Clone(current.Models)}
	for prefix, models := range next.Models {
		next.Models[prefix] = maps.Clone(models)
	}
	mutate(next)
	catalog.snapshot.Store(next)
	return next
}

func TestEmbeddedOpenCodeSnapshot(t *testing.T) {
	if openCodeBootstrap.Updated != (time.Time{}) || !validOpenCodeSnapshotData(openCodeBootstrap) {
		t.Fatal("embedded provider snapshot is invalid or masquerades as a fresh cache")
	}
	for _, prefix := range []string{"opencode-go", "opencode-zen"} {
		var described, priced bool
		for _, metadata := range openCodeBootstrap.Models[prefix] {
			described = described || metadata.Description != ""
			priced = priced || metadata.Cost.Input != nil && metadata.Cost.Output != nil
		}
		if !described || !priced {
			t.Fatalf("%s snapshot omitted provider metadata or prices", prefix)
		}
	}
}

func TestOpenCodeOnlineCatalogRefresh(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var generation atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("catalog received credentials")
		}
		if generation.Load() == 3 {
			fmt.Fprint(w, `{`)
			return
		}
		if r.URL.Path != "/metadata" {
			fmt.Fprint(w, `{"data":[{"id":"brand-new"},{"id":"deepseek-flash"},{"id":"unknown-format"}]}`)
			return
		}
		npm, cost := "@ai-sdk/openai-compatible", 1
		if generation.Load() == 2 {
			npm, cost = "@ai-sdk/openai", 3
		}
		forProvider := map[string]any{
			"npm": "@ai-sdk/anthropic",
			"models": map[string]any{
				"brand-new": map[string]any{
					"description":       fmt.Sprintf("Upstream description %d.", generation.Load()),
					"reasoning_options": []any{map[string]any{"type": "effort", "values": []string{"low", "high", "max"}}},
					"provider":          map[string]string{"npm": npm, "api": "https://untrusted.invalid"},
					"limit":             map[string]int{"context": 123456},
					"modalities":        map[string]any{"input": []string{"text", "image", "audio"}},
					"cost":              map[string]int{"input": cost, "output": 5, "cache_read": 0, "cache_write": 2},
				},
				"removed":        map[string]any{"limit": map[string]int{"context": 100}},
				"unknown-format": map[string]any{"provider": map[string]string{"npm": "@ai-sdk/future"}},
			},
		}
		data, _ := json.Marshal(map[string]any{"opencode-go": forProvider, "opencode": forProvider})
		w.Write(data)
	}))
	defer server.Close()
	catalog := newOpenCodeCatalog()
	storeOpenCodeTestSnapshot(catalog, func(snapshot *openCodeSnapshot) {
		legacy := snapshot.Models["opencode-go"]["deepseek-flash"]
		legacy.Format = "chat"
		snapshot.Models["opencode-go"]["deepseek-flash"] = legacy
	})
	catalog.metadataURL = server.URL + "/metadata"
	catalog.modelURLs = map[string]string{"opencode-go": server.URL + "/go", "opencode-zen": server.URL + "/zen"}
	config := OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "private-key"}, catalog: catalog}
	service := config.services()[0]
	if err := catalog.refresh(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("fetches = %d", requests.Load())
	}
	model, ok := service.model("brand-new")
	if !ok || model.Context != 123456 || strings.Join(model.Modalities, ",") != "text,image" || service.format("brand-new") != "chat" {
		t.Fatalf("new metadata unavailable: %+v", model)
	}
	if _, ok := service.model("glm-5.3"); ok {
		t.Fatal("removed model survived online availability replacement")
	}
	if _, ok := service.model("removed"); ok {
		t.Fatal("metadata-only model advertised as available")
	}
	if service.format("deepseek-flash") != "chat" {
		t.Fatal("live legacy alias lost last known format")
	}
	if _, err := translateChatRequest([]byte(`{"model":"opencode-go:unknown-format","input":[]}`), &service); err == nil {
		t.Fatal("unknown protocol silently treated as Chat")
	}
	body, err := ProviderModelCatalog([]byte(`{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2"}]}`), false, config)
	if err != nil || !strings.Contains(string(body), "opencode-go:brand-new") || strings.Contains(string(body), "unknown-format") {
		t.Fatalf("dynamic Codex catalog: %s, %v", body, err)
	}
	if !strings.Contains(string(body), `"description":"Upstream description 0."`) {
		t.Fatal("upstream model description not preserved in Codex catalog")
	}
	if !strings.Contains(string(body), `"effort":"max"`) || !strings.Contains(string(body), `"default_reasoning_level":null`) {
		t.Fatal("online reasoning controls not exposed to native Codex catalog")
	}
	counts := tokenCounts{InputTokens: 100, UncachedInputTokens: 80, CacheWriteTokens: 10, OutputTokens: 20}
	cost := catalog.current().Models["opencode-go"]["brand-new"].Cost.estimate("", counts)
	if !cost.known || math.Abs(cost.uncachedInput-90.0/1e6) > 1e-12 || cost.cachedInput != 0 || cost.output != 100.0/1e6 {
		t.Fatalf("online pricing: %+v", cost)
	}
	pinned := service.pin()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := catalog.refresh(t.Context(), false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if requests.Load() != 3 {
		t.Fatal("fresh cache did network I/O")
	}
	generation.Store(2)
	if err := catalog.refresh(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if service.format("brand-new") != "responses" || pinned.format("brand-new") != "chat" {
		t.Fatal("format update lost, or changed an in-flight request")
	}
	tr, err := translateChatRequest([]byte(`{"model":"opencode-go:brand-new","input":[]}`), &service)
	if err != nil || tr.format != "responses" || tr.body["input"] == nil {
		t.Fatalf("changed API not selected: %v", err)
	}
	cost = catalog.current().Models["opencode-go"]["brand-new"].Cost.estimate("", counts)
	if !cost.known || math.Abs(cost.uncachedInput-230.0/1e6) > 1e-12 {
		t.Fatalf("changed price not used: %+v", cost)
	}
	usage := newThreadUsage()
	price := catalog.current().Models["opencode-go"]["brand-new"].Cost
	usage.add("thread", "opencode-go:brand-new", "", counts, false, &price)
	report, ok := usage.snapshot("thread")
	if !ok || report.cost != cost {
		t.Fatal("online prices not used by thread totals")
	}
	loaded := newOpenCodeCatalog()
	if loaded.current().Models["opencode-go"]["brand-new"].Format != "responses" {
		t.Fatal("restart lost cached metadata")
	}
	if loaded.current().Models["opencode-go"]["brand-new"].Description != "Upstream description 2." {
		t.Fatal("description update did not survive cache reload")
	}
	updated, err := ProviderModelCatalog(body, false, config)
	if err != nil || !strings.Contains(string(updated), `"description":"Upstream description 2."`) {
		t.Fatal("refreshed upstream description not used")
	}
	cached, err := os.ReadFile(catalog.path)
	if err != nil || strings.Contains(string(cached), "private-key") || strings.Contains(string(cached), "untrusted") {
		t.Fatal("cache contains credential/endpoint data")
	}
	generation.Store(3)
	before := catalog.current()
	if err := catalog.refresh(t.Context(), true); err == nil || catalog.current() != before {
		t.Fatal("malformed refresh replaced last known good snapshot")
	}
	if err := catalog.refresh(t.Context(), false); err != nil {
		t.Fatal("failure backoff did not retain usable cache")
	}
}

func TestOpenCodeCatalogExpiryAndOffline(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog := newOpenCodeCatalog()
	expired := *catalog.current()
	expired.Updated = time.Now().Add(-2 * openCodeCatalogTTL)
	if err := catalog.save(&expired); err != nil {
		t.Fatal(err)
	}
	loaded := newOpenCodeCatalog()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	loaded.metadataURL = server.URL
	before := loaded.current()
	if err := loaded.refresh(t.Context(), false); err == nil || calls.Load() != 1 || loaded.current() != before {
		t.Fatal("expired cache did not attempt refresh and survive failure")
	}
	if err := loaded.refresh(t.Context(), false); err != nil || calls.Load() != 1 {
		t.Fatal("failed refresh did not back off")
	}
	legacy := *before
	legacy.Version = 1
	if validOpenCodeSnapshot(&legacy) {
		t.Fatal("pre-description cache version accepted as fresh")
	}
	if err := os.WriteFile(catalog.path, []byte(`{"version":1,"models":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(newOpenCodeCatalog().current().Models["opencode-go"]) != len(openCodeBootstrap.Models["opencode-go"]) {
		t.Fatal("corrupt disk cache did not fall back to bootstrap")
	}
}

func TestOpenCodeOnlinePrices(t *testing.T) {
	price := openCodePrice{
		Input: new(1.0), Output: new(2.0), CacheRead: new(.1), CacheWrite: new(1.25),
		Tiers:           jsontext.Value(`[{"input":3,"output":4,"cache_read":0.3,"cache_write":3.75,"tier":{"type":"context","size":512000}}]`),
		ContextOver200K: &openCodePrice{Input: new(99.0)},
	}
	for _, test := range []struct {
		input uint64
		rate  float64
	}{{200001, 1}, {512000, 1}, {512001, 3}} {
		actual, ok := price.forInput(test.input)
		if !ok || *actual.Input != test.rate {
			t.Fatalf("tier at %d: %+v, %v", test.input, actual, ok)
		}
	}
	price.Tiers = nil
	actual, ok := price.forInput(200001)
	if !ok || *actual.Input != 99 {
		t.Fatal("legacy long-context rate not recognized")
	}
	price.Tiers = jsontext.Value(`[{"input":3,"output":4,"tier":{"type":"future","size":1}}]`)
	if _, ok := price.forInput(100); ok {
		t.Fatal("unknown price tier treated as flat")
	}
	catalog := &openCodeCatalog{}
	catalog.snapshot.Store(&openCodeSnapshot{Models: map[string]map[string]openCodeMetadata{
		"opencode-go": {
			"free":    {Cost: openCodePrice{Input: new(0.0), Output: new(0.0)}},
			"missing": {},
		},
	}})
	counts := tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2}
	if !catalog.current().Models["opencode-go"]["free"].Cost.estimate("", counts).known {
		t.Fatal("explicit zero price is not free")
	}
	counts.UncachedInputTokens = 5
	if catalog.current().Models["opencode-go"]["free"].Cost.estimate("", counts).known || catalog.current().Models["opencode-go"]["missing"].Cost.estimate("", counts).known {
		t.Fatal("missing rates treated as zero")
	}
	if catalog.current().Models["opencode-go"]["free"].Cost.estimate("priority", tokenCounts{}).known {
		t.Fatal("unsupported service tier priced")
	}
}

func TestOpenCodeCatalogLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_OPENCODE_CATALOG_LIVE") != "1" {
		t.Skip("public catalog network check is opt-in; no inference or credentials")
	}
	catalog := newOpenCodeCatalog()
	catalog.path = filepath.Join(t.TempDir(), "catalog.json")
	if err := catalog.refresh(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	service := openCodeService{prefix: "opencode-go", catalog: catalog}
	advertised := make(map[string]bool)
	for _, model := range service.models() {
		advertised[model.id] = true
	}
	for id := range catalog.current().Models["opencode-go"] {
		switch service.format(id) {
		case "chat", "anthropic", "responses":
			if !advertised[id] {
				t.Errorf("supported live Go model was not advertised: %s", id)
			}
		case "":
			if advertised[id] {
				t.Errorf("live Go model without a supported format was advertised: %s", id)
			}
		default:
			t.Errorf("live Go model has invalid format: %s", id)
		}
	}
	t.Logf("live available models: Go=%d Zen=%d; supported Go=%d", len(catalog.current().Models["opencode-go"]), len(catalog.current().Models["opencode-zen"]), len(service.models()))
}

func TestOpenCodeRefreshWaiterCancellation(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog := newOpenCodeCatalog()
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	done := make(chan error, 1)
	defer server.Close()
	defer func() { close(release); <-done }()
	catalog.metadataURL = server.URL
	go func() { done <- catalog.refresh(t.Context(), true) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	waiter := make(chan error, 1)
	go func() { waiter <- catalog.refresh(ctx, false) }()
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request waited for another request's refresh")
	}
}

func TestOpenCodeInflightPriceSurvivesRemoval(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			catalog := newOpenCodeCatalog()
			storeOpenCodeTestSnapshot(catalog, func(snapshot *openCodeSnapshot) {
				snapshot.Updated = time.Now()
				model := snapshot.Models["opencode-go"]["glm-5.3-flash"]
				model.Cost = openCodePrice{Input: new(1.0), Output: new(2.0)}
				snapshot.Models["opencode-go"]["glm-5.3-flash"] = model
			})
			service := (OpenCodeConfig{Go: OpenCodeServiceConfig{APIKey: "test"}, catalog: catalog}).services()[0]
			client := &grokClient{openCode: &service, httpClient: &http.Client{
				Transport: grokTestTransport(func(request *http.Request) (*http.Response, error) {
					// Another request refreshes/removes the model after this
					// request selected its format and prices, before completion.
					catalog.snapshot.Store(&openCodeSnapshot{Version: 2, Updated: time.Now(), Models: map[string]map[string]openCodeMetadata{}})
					return serverHTTPResponse(grokTestSSE(
						map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": "OK"}, "finish_reason": "stop"}}},
						map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4}},
					)), nil
				}),
			}}
			response, err := client.forwardExecution(t.Context(), t.Context(), openCodeTestRequest(t, service, stream), grokTestHeaders())
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			usage := newThreadUsage()
			observation := usage.observation("thread", "", "opencode-go:glm-5.3-flash", "")
			observation.openCodePrice = response.Body.(*grokResponseBody).openCodePrice
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatal(err)
			}
			observation.observe(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 4})
			report, ok := usage.snapshot("thread")
			if !ok || !report.cost.known || report.cost.uncachedInput != 10.0/1e6 || report.cost.output != 8.0/1e6 {
				t.Fatalf("in-flight price lost after removal: %+v", report)
			}
		})
	}
}

func TestOpenCodeMetadataReasoningEffort(t *testing.T) {
	for _, format := range []string{"chat", "anthropic", "responses"} {
		t.Run(format, func(t *testing.T) {
			service := openCodeService{prefix: "opencode-go", snapshot: &openCodeSnapshot{Models: map[string]map[string]openCodeMetadata{
				"opencode-go": {"new-model": {Format: format, Efforts: []string{"none", "low", "max"}}},
			}}}
			for _, effort := range []string{"", "high", "none", "low", "max"} {
				tr, err := translateChatRequest(mustTestJSON(t, map[string]any{
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
