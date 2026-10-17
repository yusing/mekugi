package router

import (
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
)

func assertAppServerSettingsRequest(t *testing.T, w *appServerTestInput, method string, params map[string]any) {
	t.Helper()
	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(w.Bytes(), &request); err != nil {
		t.Fatalf("expected exactly one settings request: %v; wire=%s", err, w.String())
	}
	if request.Method != method || !reflect.DeepEqual(request.Params, params) {
		t.Fatalf("request = %+v; want %s %+v", request, method, params)
	}
	w.Reset()
}

func TestAppServerSettingsReasoningKeys(t *testing.T) {
	for _, tt := range []struct{ name, effort, key, want string }{
		{"increase", "low", "\x1b[1;2A", "high"},
		{"decrease", "high", "\x1b[1;2B", "low"},
		{"upper boundary", "high", "\x1b[1;2A", ""},
		{"lower boundary", "low", "\x1b[1;2B", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.model, u.reasoningEffort, u.Text = "test", tt.effort, "keep draft"
			if err := json.Unmarshal([]byte(`[{"model":"test","defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}]`), &u.models); err != nil {
				t.Fatal(err)
			}
			appServerTestKeys(t, u, tt.key)
			if tt.want == "" {
				if w.Len() != 0 || u.Settings.Pending() {
					t.Fatal("boundary wrapped or submitted settings")
				}
			} else {
				assertAppServerSettingsRequest(t, w, "thread/settings/update", map[string]any{"threadId": "main", "effort": tt.want})
			}
			if u.Text != "keep draft" || u.reasoningEffort != tt.effort {
				t.Fatal("shortcut changed draft or authoritative effort optimistically")
			}
		})
	}
}

func TestAppServerSettingsSlashControls(t *testing.T) {
	for _, tt := range []struct {
		command, field string
		value          any
	}{
		{"/model custom", "model", "custom"},
		{"/reasoning high", "effort", "high"},
		{"/effort high", "effort", "high"},
		{"/tier priority", "serviceTier", "priority"},
		{"/tier default", "serviceTier", nil},
	} {
		for _, turn := range []string{"", "active"} {
			t.Run(tt.command+"/"+turn, func(t *testing.T) {
				u, w := newAppServerTestUI()
				u.Turn, u.model, u.reasoningEffort, u.serviceTier = turn, "original", "low", "flex"
				appServerTestKeys(t, u, tt.command+"\r")
				assertAppServerSettingsRequest(t, w, "thread/settings/update", map[string]any{"threadId": "main", tt.field: tt.value})
				if u.model != "original" || u.reasoningEffort != "low" || u.serviceTier != "flex" || u.attached.conversation.Submitted[u.Submission().ID].Text != "" || !u.Settings.Pending() {
					t.Fatal("command submitted conversation or changed metadata before notification")
				}
				appServerTestKeys(t, u, "next prompt\r")
				if w.Len() != 0 || u.Text != "" || len(u.attached.conversation.Unsent) != 1 || u.attached.conversation.Unsent[0].Text != "next prompt" {
					t.Fatal("pending settings allowed conversation submission or lost stacked input")
				}
			})
		}
	}
}

func TestAppServerSettingsAuthoritativeLiveUpdate(t *testing.T) {
	for _, tt := range []struct{ name, response, notice string }{
		{"applied", `{"id":2,"result":{"status":"applied"}}`, "subsequent steps"},
		{"finished", `{"id":2,"result":{"status":"targetUnavailable"}}`, "next turn"},
		{"error", `{"id":2,"error":{"code":-1,"message":"live rejected"}}`, "live rejected"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.Turn, u.model, u.reasoningEffort = "active", "old", "low"
			appServerTestKeys(t, u, "/reasoning high\r")
			assertAppServerSettingsRequest(t, w, "thread/settings/update", map[string]any{"threadId": "main", "effort": "high"})
			appServerTestMessage(t, u, `{"id":1,"result":{}}`)
			if !u.Settings.Pending() || u.reasoningEffort != "low" || w.Len() != 0 {
				t.Fatal("enqueue acknowledgement was treated as applied settings")
			}
			appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"other","threadSettings":{"model":"foreign","effort":"high","serviceTier":"priority"}}}`)
			if u.model != "old" || u.reasoningEffort != "low" || w.Len() != 0 {
				t.Fatal("foreign settings notification affected selected thread")
			}
			appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"host-model","effort":"high","serviceTier":"priority"}}}`)
			if u.model != "host-model" || u.reasoningEffort != "high" || u.serviceTier != "priority" {
				t.Fatal("authoritative metadata not adopted")
			}
			assertAppServerSettingsRequest(t, w, "turn/settings/update", map[string]any{"threadId": "main", "turnId": "active", "effort": "high"})
			appServerTestMessage(t, u, tt.response)
			if u.Settings.Pending() || !strings.Contains(u.attached.feedback.Text, tt.notice) || u.reasoningEffort != "high" {
				t.Fatalf("live result state: pending=%v effort=%s notice=%q", u.Settings.Pending(), u.reasoningEffort, u.attached.feedback.Text)
			}
			if w.Len() != 0 || u.attached.conversation.Submitted[u.Submission().ID].Text != "" {
				t.Fatal("live settings response submitted conversation")
			}
		})
	}
}

func TestAppServerSettingsFinishedTargetAndThreadError(t *testing.T) {
	t.Run("finished target", func(t *testing.T) {
		u, w := newAppServerTestUI()
		u.Turn = "old-turn"
		appServerTestKeys(t, u, "/model next\r")
		w.Reset()
		u.Turn = "new-turn"
		appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"next","effort":null,"serviceTier":null}}}`)
		if w.Len() != 0 || u.Settings.Pending() || u.model != "next" {
			t.Fatal("settings retargeted another turn or stayed pending")
		}
	})
	t.Run("thread rejected", func(t *testing.T) {
		u, w := newAppServerTestUI()
		u.model = "old"
		appServerTestKeys(t, u, "/model next\r")
		w.Reset()
		appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"invalid model"}}`)
		if w.Len() != 0 || u.Settings.Pending() || u.model != "old" || !strings.Contains(u.attached.feedback.Text, "invalid model") {
			t.Fatal("thread error changed metadata, emitted live RPC, or left pending guard")
		}
	})
}

func TestAppServerSettingsPendingCommandRetainsDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/reasoning high\r")
	w.Reset()
	appServerTestKeys(t, u, "/tier priority\r")
	if u.Text != "/tier priority" || w.Len() != 0 {
		t.Fatalf("pending command lost: %q wire=%q", u.Text, w.String())
	}
}

func TestAppServerSettingsModelListPagination(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestRequest(t, u, "1", "model/list")
	appServerTestMessage(t, u, `{"id":1,"result":{"data":[{"model":"one"}],"nextCursor":"next"}}`)
	assertAppServerSettingsRequest(t, w, "model/list", map[string]any{"cursor": "next", "includeHidden": true})
	appServerTestMessage(t, u, `{"id":1,"result":{"data":[{"model":"two"}],"nextCursor":null}}`)
	if len(u.models) != 2 || u.models[0].Model != "one" || u.models[1].Model != "two" {
		t.Fatalf("catalog=%+v", u.models)
	}
	appServerTestKeys(t, u, "/model\r")
	if (u.picker.Kind != "settings" || len(u.picker.Choices) != 2 || u.picker.Choices[0].Name != "one" || u.picker.Choices[1].Name != "two") || w.Len() != 0 {
		t.Fatalf("choices=%q", u.attached.feedback.Text)
	}
}

func TestAppServerSettingsReasoningWaitsForCatalog(t *testing.T) {
	u, w := newAppServerTestUI()
	u.model, u.reasoningEffort, u.modelsLoading = "test", "low", true
	appServerTestRequest(t, u, "99", "model/list")
	appServerTestKeys(t, u, "\x1b[1;2A")
	if w.Len() != 0 || u.reasoningKey == nil {
		t.Fatal("shortcut was lost or sent without metadata")
	}
	appServerTestMessage(t, u, `{"id":99,"result":{"data":[{"model":"test","defaultReasoningEffort":"low","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}]}}`)
	assertAppServerSettingsRequest(t, w, "thread/settings/update", map[string]any{"threadId": "main", "effort": "high"})
	if u.modelsLoading || u.reasoningKey != nil {
		t.Fatal("model loading not settled")
	}
}

func TestAppServerSettingsUnchangedDoesNotWaitForNotification(t *testing.T) {
	for _, turn := range []string{"", "active"} {
		u, w := newAppServerTestUI()
		u.model, u.reasoningEffort, u.Turn = "model", "high", turn
		appServerTestKeys(t, u, "/reasoning high\r")
		if turn != "" {
			assertAppServerSettingsRequest(t, w, "turn/settings/update", map[string]any{"threadId": "main", "turnId": "active", "effort": "high"})
			appServerTestMessage(t, u, `{"id":1,"result":{"status":"applied"}}`)
		} else if w.Len() != 0 {
			t.Fatalf("no-op wrote thread update: %q", w.String())
		}
		if u.Settings.Pending() || u.Text != "" {
			t.Fatal("no-op command remains pending")
		}
		appServerTestKeys(t, u, "prompt\r")
		if u.attached.conversation.Submitted[u.Submission().ID].Text != "prompt" {
			t.Fatal("no-op blocked prompt")
		}
	}
}

func TestAppServerSettingsChoicesRenderedInPicker(t *testing.T) {
	u, _ := newAppServerTestUI()
	if err := json.Unmarshal([]byte(`[{"model":"first-choice"},{"model":"last-choice-that-would-not-fit-in-the-status-line"}]`), &u.models); err != nil {
		t.Fatal(err)
	}
	appServerTestKeys(t, u, "/model\r")
	frame, _ := u.testMainFrame(55, 20, 0)
	rendered := strings.Join(frame, "\n")
	if !strings.Contains(rendered, "first-choice") || !strings.Contains(rendered, "last-choice-that-would-not-fit") {
		t.Fatalf("missing choices: %s", rendered)
	}
	if len(u.attached.source.Entries) != 0 || len(u.InputHistory) != 0 || u.attached.conversation.Submitted[u.Submission().ID].Text != "" {
		t.Fatal("local choices submitted to host")
	}
}

func TestAppServerSettingsTierAliasNoop(t *testing.T) {
	for _, turn := range []string{"", "active"} {
		u, w := newAppServerTestUI()
		u.serviceTier, u.Turn = "priority", turn
		appServerTestKeys(t, u, "/tier fast\r")
		if turn != "" {
			assertAppServerSettingsRequest(t, w, "turn/settings/update", map[string]any{"threadId": "main", "turnId": "active", "serviceTier": "fast"})
			appServerTestMessage(t, u, `{"id":1,"result":{"status":"applied"}}`)
		} else if w.Len() != 0 {
			t.Fatal("alias queued unchanged thread settings")
		}
		appServerTestKeys(t, u, "prompt\r")
		if u.Settings.Pending() || u.attached.conversation.Submitted[u.Submission().ID].Text != "prompt" {
			t.Fatal("alias no-op blocked prompt")
		}
	}
}

func TestAppServerSettingsPickerSelection(t *testing.T) {
	for _, command := range []string{"/model", "/effort", "/reasoning", "/tier"} {
		for _, turn := range []string{"", "active"} {
			t.Run(command+turn, func(t *testing.T) {
				u, w := newAppServerTestUI()
				u.model, u.reasoningEffort, u.serviceTier, u.Turn = "one", "low", "priority", turn
				if err := json.Unmarshal([]byte(`[{"model":"one","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}],"serviceTiers":[{"id":"priority"}]},{"model":"two"},{"model":"hidden","hidden":true}]`), &u.models); err != nil {
					t.Fatal(err)
				}
				appServerTestKeys(t, u, command+"\r")
				if u.picker.Kind != "settings" || len(u.picker.Choices) != 2 || u.Text != "" || w.Len() != 0 {
					t.Fatalf("picker did not open locally: %+v", u.picker)
				}
				appServerTestKeys(t, u, "\x1b[B\r")
				field, value := "effort", any("high")
				switch command {
				case "/model":
					field, value = "model", "two"
				case "/tier":
					field, value = "serviceTier", nil
				}
				assertAppServerSettingsRequest(t, w, "thread/settings/update", map[string]any{"threadId": "main", field: value})
				if u.picker.Open || u.model != "one" || u.reasoningEffort != "low" || u.serviceTier != "priority" || len(u.attached.source.Entries) != 0 || len(u.InputHistory) != 0 || u.attached.conversation.Submitted[u.Submission().ID].Text != "" {
					t.Fatal("selection polluted transcript or changed confirmed settings")
				}
			})
		}
	}
}

func TestAppServerSettingsPickerLoadingAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		u, w := newAppServerTestUI()
		u.modelsLoading = true
		appServerTestRequest(t, u, "99", "model/list")
		appServerTestKeys(t, u, "/model\r")
		if !u.picker.Loading || u.picker.Kind != "settings" {
			t.Fatal("missing loading picker")
		}
		appServerTestKeys(t, u, "\r")
		if cancel {
			appServerTestKeys(t, u, "\x03")
			appServerTestKeys(t, u, "retained draft")
		}
		appServerTestMessage(t, u, `{"id":99,"result":{"data":[{"model":"one"}]}}`)
		if cancel {
			if u.picker.Open || u.Text != "retained draft" {
				t.Fatal("late catalog reopened cancelled picker or changed draft")
			}
		} else if u.picker.Loading || len(u.picker.Choices) != 1 {
			t.Fatal("catalog did not populate picker")
		}
		if w.Len() != 0 || len(u.attached.source.Entries) != 0 {
			t.Fatal("loading or cancellation submitted input")
		}
	}
}

func TestAppServerSettingsPickerEmptyFailureAndPaste(t *testing.T) {
	u, w := newAppServerTestUI()
	appServerTestKeys(t, u, "/effort\r\x1b[A\x1b[B\x1b[6~\r")
	if u.picker.Problem == "" || w.Len() != 0 {
		t.Fatal("empty catalog lacks feedback or submits")
	}
	appServerTestKeys(t, u, "\x1b[200~do not submit\x1b[201~")
	if u.Text != "" || u.picker.Kind != "settings" {
		t.Fatal("paste escaped modal")
	}
	u.pickerKey("\x1b")
	u.refreshPicker()
	if u.picker.Open || u.picker.Setting != "" {
		t.Fatal("Escape did not cancel")
	}
	u.modelsLoading = true
	appServerTestRequest(t, u, "99", "model/list")
	appServerTestKeys(t, u, "/model\r")
	appServerTestMessage(t, u, `{"id":99,"error":{"code":-1,"message":"unavailable"}}`)
	if u.picker.Loading || u.picker.Problem == "" || len(u.attached.source.Entries) != 0 {
		t.Fatal("failed catalog lacks local feedback")
	}
}
