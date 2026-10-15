package opencode

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
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

func storeOpenCodeTestSnapshot(catalog *Catalog, mutate func(*openCodeSnapshot)) *openCodeSnapshot {
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
	cachePath := filepath.Join(t.TempDir(), "catalog.json")
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
	catalog := NewCatalog(cachePath, nil)
	storeOpenCodeTestSnapshot(catalog, func(snapshot *openCodeSnapshot) {
		legacy := snapshot.Models["opencode-go"]["deepseek-flash"]
		legacy.Format = "chat"
		snapshot.Models["opencode-go"]["deepseek-flash"] = legacy
	})
	catalog.metadataURL = server.URL + "/metadata"
	catalog.modelURLs = map[string]string{"opencode-go": server.URL + "/go", "opencode-zen": server.URL + "/zen"}
	config := Config{Go: ServiceConfig{APIKey: "private-key"}, Catalog: catalog}
	service := config.Services()[0]
	if err := catalog.Refresh(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("fetches = %d", requests.Load())
	}
	model, ok := service.Model("brand-new")
	if !ok || model.Context != 123456 || strings.Join(model.Modalities, ",") != "text,image" || service.Format("brand-new") != "chat" {
		t.Fatalf("new metadata unavailable: %+v", model)
	}
	if _, ok := service.Model("glm-5.3"); ok {
		t.Fatal("removed model survived online availability replacement")
	}
	if _, ok := service.Model("removed"); ok {
		t.Fatal("metadata-only model advertised as available")
	}
	if service.Format("deepseek-flash") != "chat" {
		t.Fatal("live legacy alias lost last known format")
	}
	if service.Format("unknown-format") != "" {
		t.Fatal("unknown protocol advertised")
	}
	if model.Description != "Upstream description 0." || strings.Join(model.Efforts, ",") != "low,high,max" {
		t.Fatal("online description/efforts lost")
	}
	if *model.Cost.Input != 1 {
		t.Fatal("online price lost")
	}
	pinned := service.Pin()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := catalog.Refresh(t.Context(), false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if requests.Load() != 3 {
		t.Fatal("fresh cache did network I/O")
	}
	generation.Store(2)
	if err := catalog.Refresh(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if service.Format("brand-new") != "responses" || pinned.Format("brand-new") != "chat" {
		t.Fatal("format update lost, or changed an in-flight request")
	}
	model, _ = service.Model("brand-new")
	if *model.Cost.Input != 3 {
		t.Fatal("changed price lost")
	}
	loaded := NewCatalog(cachePath, nil)
	if loaded.current().Models["opencode-go"]["brand-new"].Format != "responses" {
		t.Fatal("restart lost cached metadata")
	}
	if loaded.current().Models["opencode-go"]["brand-new"].Description != "Upstream description 2." {
		t.Fatal("description update did not survive cache reload")
	}
	cached, err := os.ReadFile(catalog.path)
	if err != nil || strings.Contains(string(cached), "private-key") || strings.Contains(string(cached), "untrusted") {
		t.Fatal("cache contains credential/endpoint data")
	}
	generation.Store(3)
	before := catalog.current()
	if err := catalog.Refresh(t.Context(), true); err == nil || catalog.current() != before {
		t.Fatal("malformed refresh replaced last known good snapshot")
	}
	if err := catalog.Refresh(t.Context(), false); err != nil {
		t.Fatal("failure backoff did not retain usable cache")
	}
}

func TestOpenCodeCatalogExpiryAndOffline(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "catalog.json")
	catalog := NewCatalog(cachePath, nil)
	expired := *catalog.current()
	expired.Updated = time.Now().Add(-2 * openCodeCatalogTTL)
	if err := catalog.save(&expired); err != nil {
		t.Fatal(err)
	}
	loaded := NewCatalog(cachePath, nil)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	loaded.metadataURL = server.URL
	before := loaded.current()
	if err := loaded.Refresh(t.Context(), false); err == nil || calls.Load() != 1 || loaded.current() != before {
		t.Fatal("expired cache did not attempt refresh and survive failure")
	}
	if err := loaded.Refresh(t.Context(), false); err != nil || calls.Load() != 1 {
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
	if len(NewCatalog(cachePath, nil).current().Models["opencode-go"]) != len(openCodeBootstrap.Models["opencode-go"]) {
		t.Fatal("corrupt disk cache did not fall back to bootstrap")
	}
}

func TestOpenCodeOnlinePrices(t *testing.T) {
	price := Price{
		Input: new(1.0), Output: new(2.0), CacheRead: new(.1), CacheWrite: new(1.25),
		Tiers:           jsontext.Value(`[{"input":3,"output":4,"cache_read":0.3,"cache_write":3.75,"tier":{"type":"context","size":512000}}]`),
		ContextOver200K: &Price{Input: new(99.0)},
	}
	for _, test := range []struct {
		input uint64
		rate  float64
	}{{200001, 1}, {512000, 1}, {512001, 3}} {
		actual, ok := price.ForInput(test.input)
		if !ok || *actual.Input != test.rate {
			t.Fatalf("tier at %d: %+v, %v", test.input, actual, ok)
		}
	}
	price.Tiers = nil
	actual, ok := price.ForInput(200001)
	if !ok || *actual.Input != 99 {
		t.Fatal("legacy long-context rate not recognized")
	}
	price.Tiers = jsontext.Value(`[{"input":3,"output":4,"tier":{"type":"future","size":1}}]`)
	if _, ok := price.ForInput(100); ok {
		t.Fatal("unknown price tier treated as flat")
	}
}

func TestOpenCodeCatalogLive(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_OPENCODE_CATALOG_LIVE") != "1" {
		t.Skip("public catalog network check is opt-in; no inference or credentials")
	}
	catalog := NewCatalog(filepath.Join(t.TempDir(), "catalog.json"), nil)
	if err := catalog.Refresh(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	service := Service{Prefix: "opencode-go", Catalog: catalog}
	advertised := make(map[string]bool)
	for _, model := range service.Models() {
		advertised[model.ID] = true
	}
	for id := range catalog.current().Models["opencode-go"] {
		switch service.Format(id) {
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
	t.Logf("live available models: Go=%d Zen=%d; supported Go=%d", len(catalog.current().Models["opencode-go"]), len(catalog.current().Models["opencode-zen"]), len(service.Models()))
}

func TestOpenCodeRefreshWaiterCancellation(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "catalog.json")
	catalog := NewCatalog(cachePath, nil)
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
	go func() { done <- catalog.Refresh(t.Context(), true) }()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	waiter := make(chan error, 1)
	go func() { waiter <- catalog.Refresh(ctx, false) }()
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
