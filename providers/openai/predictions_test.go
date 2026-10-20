package openai

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

func TestPredictionSettingsDedicatedWebSocketAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get(AccountIDHeader) != "account" {
			t.Error("missing dedicated connection authentication")
		}
		switch r.URL.Path {
		case "/codex/responses":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
		case "/wham/predictions/config":
			_, _ = w.Write([]byte(`{"is_enabled":true,"prompt":"remote prompt"}`))
		case "/wham/statsig/bootstrap":
			_, _ = w.Write([]byte(`{"statsigPayload":"{}"}`))
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/codex", server.Client())
	conn, _, err := client.Dial(t.Context(), http.Header{"Authorization": {"Bearer fixture"}, AccountIDHeader: {"account"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if client.PredictionIdentity() == 0 {
		t.Fatal("dedicated connection did not capture authenticated identity")
	}
	config, err := client.PredictionSettings(t.Context())
	if err != nil || !config.Enabled || config.Prompt != "remote prompt" {
		t.Fatalf("dedicated connection authentication unavailable: config=%+v err=%v", config, err)
	}
}

func TestPredictionSettingsAuthenticatedPolicyAndCache(t *testing.T) {
	var configs, bootstraps atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get(AccountIDHeader) == "" {
			t.Error("missing snapshot authentication")
		}
		switch r.URL.Path {
		case "/codex/models":
			_, _ = w.Write([]byte(`{}`))
		case "/wham/predictions/config":
			configs.Add(1)
			_, _ = w.Write([]byte(`{"is_enabled":true,"prompt":"remote prompt","prompt_version":"","prediction_reasoning_effort":"low","sample_rate":0.5,"live_sample_rate":1,"catch_up_sample_rate":0,"unsupported_models":["unsupported"]}`))
		case "/wham/statsig/bootstrap":
			bootstraps.Add(1)
			if r.Method != http.MethodPost {
				t.Error("bootstrap must POST")
			}
			var body map[string]string
			if json.UnmarshalRead(r.Body, &body) != nil || body["window_type"] != "electron" || body["brand_name"] != "chatgpt" {
				t.Error("wrong desktop bootstrap context")
			}
			_ = json.MarshalWrite(w, map[string]string{"statsigPayload": `{"dynamic_configs":{"3941626706":{"value":{"st":{"risk":0.7},"ubl":["blocked-user"],"abl":["blocked-assistant"]}}}}`})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(server.URL+"/codex", server.Client())
	if _, err := client.PredictionSettings(t.Context()); err == nil {
		t.Fatal("used absent authentication")
	}
	headers := http.Header{"Authorization": {"Bearer fixture"}, AccountIDHeader: {"account-one"}}
	response, err := client.Models(t.Context(), headers, "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	first := client.PredictionIdentity()
	for range 2 {
		config, err := client.PredictionSettings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if config.Prompt != "remote prompt" || config.Effort != "low" || config.Rate("live") != 1 || config.Rate("catch_up") != 0 {
			t.Fatalf("lost remote settings: %+v", config)
		}
		for _, metadata := range []string{`{"prompt":{"a":{"outputs":[{"results":[{"_modapi_scores":{"risk":0.7}}]}]}}}`, `{"generation":{"a":{"outputs":[{"results":[{"labels":["blocked-assistant"]}]}]}}}`} {
			if !config.Policy.Suppresses(jsontext.Value(metadata)) {
				t.Fatal("remote policy was not applied")
			}
		}
		if config.Policy.Suppresses(jsontext.Value(`{"generation":{"a":{"outputs":[{"results":[{"_modapi_scores":{"risk":1}}]}]}}}`)) {
			t.Fatal("prompt thresholds applied to generation")
		}
	}
	if configs.Load() != 1 || bootstraps.Load() != 1 {
		t.Fatal("configuration was not cached")
	}
	headers.Set(AccountIDHeader, "account-two")
	response, err = client.Models(t.Context(), headers, "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if client.PredictionIdentity() == first {
		t.Fatal("account change retained identity")
	}
	if _, err = client.PredictionSettings(t.Context()); err != nil {
		t.Fatal(err)
	}
	if configs.Load() != 2 {
		t.Fatal("account change reused configuration")
	}
}

func TestPredictionSettingsRejectRedirectAndInvalidConfig(t *testing.T) {
	for _, body := range []string{"redirect", `{"is_enabled":true}`, `{"sample_rate":2}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body == "redirect" {
					http.Redirect(w, r, "/unexpected", http.StatusFound)
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := New(server.URL, server.Client())
			client.predictions.observe(http.Header{"Authorization": {"Bearer fixture"}, AccountIDHeader: {"account"}})
			if _, err := client.PredictionSettings(t.Context()); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	if (PredictionPolicy{}).Suppresses(jsontext.Value(strings.Repeat("x", 4))) {
		t.Fatal("absent policy invented suppression")
	}
}
