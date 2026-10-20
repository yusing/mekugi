package router

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestGrokLaunchCatalogNamespaces(t *testing.T) {
	for _, unprefixed := range []bool{false, true} {
		t.Run(fmt.Sprint(unprefixed), func(t *testing.T) {
			session := Session{GrokEnabled: true, GrokUnprefixed: unprefixed, ThirdPartyOnly: true}
			body, err := ProviderModelCatalog([]byte(`{"models":[{"slug":"gpt-6-sol","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform"}]}`), session)
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Models []struct {
					Slug  string `json:"slug"`
					Shell string `json:"shell_type"`
				} `json:"models"`
			}
			if err := json.Unmarshal(body, &catalog); err != nil || len(catalog.Models) != len(grokModels) {
				t.Fatalf("third-party-only catalog: %s, %v", body, err)
			}
			for i, entry := range catalog.Models {
				want := grokModels[i]
				if !unprefixed {
					want = "grok:" + want
				}
				if entry.Slug != want || entry.Shell != "unified_exec" {
					t.Fatalf("catalog model = %+v, want %s", entry, want)
				}
			}
			repeated, err := ProviderModelCatalog(body, session)
			if err != nil || !bytes.Equal(body, repeated) {
				t.Fatalf("session catalog is not idempotent: %v", err)
			}
		})
	}
}

func TestGrokLaunchRejectsWrongNamespacesAndOpenAI(t *testing.T) {
	for _, unprefixed := range []bool{false, true} {
		provider := newProviderClient("http://unused.invalid", nil)
		provider.thirdPartyOnly = true
		provider.grok = &grokClient{unprefixed: unprefixed, auth: newGrokAuth("", "test"), httpClient: &http.Client{Transport: grokTestTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("wrong-mode model reached a provider")
			return nil, nil
		})}}
		wrongModel := "grok-4.7"
		if unprefixed {
			wrongModel = "grok:grok-4.7"
		}
		for _, model := range []string{wrongModel, "gpt-6-sol"} {
			_, err := provider.forwardExecution(t.Context(), t.Context(), mustTestJSON(t, map[string]any{"model": model, "input": []any{}}), grokTestHeaders(), "")
			if _, ok := err.(*requestCompatibilityError); !ok {
				t.Fatalf("wrong-mode %s did not reject locally: %v", model, err)
			}
		}
	}
}

func TestGrokDedicatedSpawnGuidance(t *testing.T) {
	request := bridgeTestRequest(t, false)
	if _, err := prepareSubagentBridge(&request, true, true); err != nil {
		t.Fatal(err)
	}
	for _, model := range grokModels {
		if !bytes.Contains(request.fields["tools"], []byte("`"+model+"`")) {
			t.Fatalf("dedicated Grok model missing from spawn guidance: %s", model)
		}
	}
	if bytes.Contains(request.fields["tools"], []byte("grok:")) {
		t.Fatal("dedicated Grok spawn guidance advertises namespaced models")
	}
}

func TestThirdPartyAutomaticModelPreservesSavedResumeSettings(t *testing.T) {
	for _, unprefixed := range []bool{false, true} {
		model := "grok-4.5"
		if !unprefixed {
			model = "grok:" + model
		}
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("plain=%t/explicit=%t", unprefixed, explicit), func(t *testing.T) {
				argv := []string{"mekugi", "--grok-auth-file", "-model-not-an-override"}
				if unprefixed {
					argv = append(argv, "grok")
				}
				argv = append(argv, "--yolo")
				chosen := "grok-4.7"
				if !unprefixed {
					chosen = "grok:" + chosen
				}
				if explicit {
					argv = append(argv, "-m", chosen)
				}
				cmdArgs := []string{"codex", "app-server", "-c", fmt.Sprintf("model=%q", chosen), "-c", `model_provider="mekugi_wrap"`}
				u, input := newAppServerTestUI()
				info := resumeSettingsRollout(t, fmt.Sprintf(`{"type":"turn_context","payload":{"model":%q,"effort":"high"}}`, model), "")
				u.thread, u.resumeThread = "", info.ID
				u.resumeConfig = appServerResumeConfig(cmdArgs, argv)
				if err := u.requestResume(info.ID); err != nil {
					t.Fatal(err)
				}
				read := resumeTestOne(t, input, "thread/read")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":%q,"path":%q}}}`, read.ID, info.ID, info.Path))
				var request struct {
					Params struct {
						Config map[string]any `json:"config"`
					} `json:"params"`
				}
				if err := json.Unmarshal(input.Bytes(), &request); err != nil {
					t.Fatal(err)
				}
				want := model
				if explicit {
					want = chosen
				}
				if request.Params.Config["model"] != want || request.Params.Config["model_provider"] != "mekugi_wrap" {
					t.Fatalf("resume lost saved/explicit selection: %v, want %s", request.Params.Config, want)
				}
			})
		}
	}
}

func TestUISnapshotGrokLaunchModelPicker(t *testing.T) {
	for _, unprefixed := range []bool{false, true} {
		name := "standalone"
		if unprefixed {
			name = "grok"
		}
		t.Run(name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			body, err := ProviderModelCatalog([]byte(`{"models":[{"slug":"gpt-6-sol","multi_agent_version":"v2"}]}`), Session{GrokEnabled: true, GrokUnprefixed: unprefixed, ThirdPartyOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			var catalog struct {
				Models []struct {
					Slug string `json:"slug"`
				} `json:"models"`
			}
			if err := json.Unmarshal(body, &catalog); err != nil {
				t.Fatal(err)
			}
			for _, entry := range catalog.Models {
				u.models = append(u.models, appServerModel{Model: entry.Slug})
			}
			u.model = "grok-4.7"
			if !unprefixed {
				u.model = "grok:" + u.model
			}
			appServerTestKeys(t, u, "/model\r")
			assertNativeUISnapshot(t, "model-picker-"+name, u.renderPicker(80, 8))
			if strings.Contains(strings.Join(u.renderPicker(80, 8), "\n"), "gpt-") {
				t.Fatal("third-party picker advertises an OpenAI model")
			}
		})
	}
}
