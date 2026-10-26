package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/conversation/presets"
)

func TestPresetsNativeConfiguration(t *testing.T) {
	for _, scenario := range []string{"read", "save", "new", "delete", "profile", "protected", "inherited-delete", "disabled", "disabled-malformed", "disabled-malformed-read", "overridden-malformed", "conflict", "invalid-read", "missing-user"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				config := `{"config":{"tui":{"presets":{"1":{"input":"native prompt","label":"Native label"}}}},"layers":[{"name":{"type":"user","file":"/config/config.toml"},"version":"base-v1","config":{"tui":{"presets":{"1":{"input":"native prompt","label":"Native label"}}}}}]}`
				var snapshot presetConfiguration
				if err := json.Unmarshal([]byte(config), &snapshot); err != nil {
					t.Fatal(err)
				}
				if scenario == "profile" || scenario == "inherited-delete" {
					profile := snapshot.Layers[0]
					profile.Name.File, profile.Version = "/config/review.config.toml", "profile-v1"
					snapshot.Layers = append(snapshot.Layers, snapshot.Layers[0])
					snapshot.Layers[0] = profile
				}
				if scenario == "protected" || strings.HasPrefix(scenario, "disabled") || scenario == "overridden-malformed" {
					project := snapshot.Layers[0]
					project.Name.Type, project.Name.File = "project", "/workspace/.codex/config.toml"
					if scenario == "overridden-malformed" {
						project.Name.Type, project.Name.File = "system", "/etc/codex/config.toml"
					}
					if strings.HasPrefix(scenario, "disabled") {
						project.DisabledReason = "untrusted project"
					}
					snapshot.Layers = append(snapshot.Layers, snapshot.Layers[0])
					snapshot.Layers[0] = project
					if strings.Contains(scenario, "malformed") {
						snapshot.Layers[0].Config = map[string]jsontext.Value{"tui": jsontext.Value(`{"presets":{"1":{"input":123}}}`)}
					}
					if scenario == "overridden-malformed" {
						snapshot.Layers[0], snapshot.Layers[1] = snapshot.Layers[1], snapshot.Layers[0]
					}
				}
				if scenario == "missing-user" {
					snapshot.Layers = nil
				}
				if scenario == "invalid-read" {
					snapshot.Config.TUI.Presets["1"] = presets.Slot{Input: "!shell"}
				}
				transport := &requestTransport{id: "preset-call"}
				requests := NewRequests[struct{}](transport)
				store := requests.Presets(t.Context(), "main", "/workspace")
				input, label, slot := "changed prompt", "Renamed", 1
				if scenario == "delete" || scenario == "inherited-delete" {
					input = ""
				}
				if scenario == "new" {
					label, slot = "", 2
				}
				done := make(chan error, 1)
				var slots [10]presets.Slot
				go func() {
					if scenario == "read" || strings.HasSuffix(scenario, "-read") {
						var err error
						slots, err = store.Load()
						done <- err
					} else {
						_, err := store.Save(slot, presets.Slot{Input: input, Label: label})
						done <- err
					}
				}()
				writes := 0
				for {
					select {
					case call := <-requests.Calls():
						requests.Dispatch(call)
						var result jsontext.Value
						var responseError *appserver.Error
						if call.method == "config/read" {
							var params struct {
								Cwd    string `json:"cwd"`
								Layers bool   `json:"includeLayers"`
							}
							if err := json.Unmarshal(call.params, &params); err != nil || params.Cwd != "/workspace" || !params.Layers {
								t.Fatal("configuration read lost workspace/layers")
							}
							result, _ = json.Marshal(snapshot)
						} else {
							writes++
							var params struct {
								Key     string        `json:"keyPath"`
								File    string        `json:"filePath"`
								Version string        `json:"expectedVersion"`
								Merge   string        `json:"mergeStrategy"`
								Value   *presets.Slot `json:"value"`
							}
							if err := json.Unmarshal(call.params, &params); err != nil {
								t.Fatal(err)
							}
							file, version := "/config/config.toml", "base-v1"
							if scenario == "profile" {
								file, version = "/config/review.config.toml", "profile-v1"
							}
							if call.method != "config/value/write" || params.File != file || params.Version != version || params.Merge != "replace" {
								t.Fatalf("wrong writer: %+v", params)
							}
							if scenario == "delete" {
								if params.Value != nil || params.Key != "tui.presets.1" {
									t.Fatal("delete did not clear only one native slot")
								}
							} else {
								key := "tui.presets.1"
								if scenario == "new" {
									key = "tui.presets.2"
								}
								// A new slot writes its empty label explicitly to mask inherited ones.
								if params.Value == nil || params.Value.Input != input || params.Value.Label != label || params.Key != key || !strings.Contains(string(call.params), `"label":`) {
									t.Fatalf("native slot changed: %+v", params)
								}
							}
							if scenario == "conflict" {
								responseError = &appserver.Error{Code: -32600, Message: "Configuration was modified since last read"}
							}
							result = jsontext.Value(`{"status":"ok","version":"new-version"}`)
						}
						requests.CompleteCall(appserver.Message{ID: jsontext.Value("preset-call"), Result: result, Error: responseError})
					case err := <-done:
						wantError := scenario == "protected" || scenario == "inherited-delete" || scenario == "conflict" || strings.HasPrefix(scenario, "invalid-") || scenario == "missing-user"
						if (err != nil) != wantError {
							t.Fatalf("error = %v", err)
						}
						wantWrites := 1
						if scenario == "read" || strings.HasSuffix(scenario, "-read") || wantError && scenario != "conflict" {
							wantWrites = 0
						}
						if writes != wantWrites {
							t.Fatalf("writes = %d, want %d", writes, wantWrites)
						}
						if (scenario == "read" || scenario == "disabled-malformed-read") && (slots[1].Input != "native prompt" || slots[1].Label != "Native label" || slots[0].Input != "") {
							t.Fatal("effective native slots were not loaded")
						}
						return
					}
				}
			})
		})
	}
}
