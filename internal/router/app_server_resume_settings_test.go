package router

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/livediff"
)

func resumeSettingsRollout(t *testing.T, records ...string) appServerThreadInfo {
	t.Helper()
	info := appServerThreadInfo{ID: "saved", Path: filepath.Join(t.TempDir(), "rollout.jsonl")}
	content := `{"type":"session_meta","payload":{"id":"saved"}}` + "\n" + strings.Join(records, "\n")
	if err := os.WriteFile(info.Path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestAppServerReadResumeSettings(t *testing.T) {
	initial := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"first","reasoning_effort":"low","service_tier":"priority"}}}`
	for _, tc := range []struct {
		name    string
		records []string
		want    map[string]any
	}{
		{"idle changes win", []string{initial, `{"type":"turn_context","payload":{"model":"turn","effort":"high"}}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"idle","reasoning_effort":"medium","service_tier":"flex"}}}`, ""}, map[string]any{"model": "idle", "model_reasoning_effort": "medium", "service_tier": "flex"}},
		{"turn preserves last tier", []string{initial, `{"type":"turn_context","payload":{"model":"turn","effort":"high"}}`, ""}, map[string]any{"model": "turn", "model_reasoning_effort": "high", "service_tier": "priority"}},
		{"cleared defaults", []string{initial, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"idle"}}}`, ""}, map[string]any{"model": "idle", "model_reasoning_effort": nil, "service_tier": nil}},
		{"older history", []string{`{"type":"turn_context","payload":{"model":"legacy","effort":"low"}}`, ""}, map[string]any{"model": "legacy", "model_reasoning_effort": "low"}},
		{"partial and invalid records ignored", []string{initial, `{broken}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"partial","service_tier":"flex"}}}`}, map[string]any{"model": "first", "model_reasoning_effort": "low", "service_tier": "priority"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readResumeSettings(resumeSettingsRollout(t, tc.records...), time.Time{}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("saved settings: got %#v, want %#v", got, tc.want)
			}
		})
	}
	info := resumeSettingsRollout(t, initial, "")
	info.ID = "other"
	if got := readResumeSettings(info, time.Time{}); len(got) != 0 {
		t.Fatalf("borrowed another thread's settings: %#v", got)
	}
}

func TestAppServerReadResumeSettingsBound(t *testing.T) {
	info := resumeSettingsRollout(t, `{"type":"turn_context","payload":{"model":"too-old","effort":"high"}}`, "")
	f, err := os.OpenFile(info.Path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Seek(9<<20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if got := readResumeSettings(info, time.Time{}); len(got) != 0 {
		t.Fatalf("read outside retention bound: %#v", got)
	}
	if _, err := f.WriteString(`{"type":"turn_context","payload":{"model":"retained","effort":"low"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if got := readResumeSettings(info, time.Time{}); got["model"] != "retained" {
		t.Fatalf("lost retained settings: %#v", got)
	}
}

func TestUISnapshotNativeResumeSettingsUnavailable(t *testing.T) {
	u, w := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.thread, u.resumeThread = "", "saved"
	if err := u.requestResume("saved"); err != nil {
		t.Fatal(err)
	}
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"saved"}}}`, read.ID))
	resumeTestOne(t, w, "thread/resume")
	rows, _ := u.mainFrame(100, 12, 0)
	assertNativeUISnapshot(t, "native-resume-settings-unavailable", rows)
}

func TestUISnapshotNativeResumeSwitchSettingsUnavailable(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	appServerTestKeys(t, u, "/resume saved\r")
	resume := resumeTestPrepared(t, u, w)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"default-model","thread":{"id":"saved","cwd":"/work","turns":[]}}}`, resume.ID))
	resumeTestSettle(t, u, w)
	if u.thread != "saved" || u.clearing || !strings.Contains(u.notice, "Saved model settings unavailable") {
		t.Fatalf("completed resume lost fallback notice: thread=%q clearing=%v notice=%q", u.thread, u.clearing, u.notice)
	}
	rows, _ := u.mainFrame(100, 12, 0)
	assertNativeUISnapshot(t, "native-resume-switch-settings-unavailable", rows)
}

func TestAppServerResumeSettingsOverridesPerField(t *testing.T) {
	info := resumeSettingsRollout(t, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"saved-model","reasoning_effort":"high","service_tier":"priority"}}}`, "")
	for _, field := range []string{"", "model", "model_reasoning_effort", "service_tier"} {
		t.Run("override="+field, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.thread, u.resumeThread = "", info.ID
			u.resumeConfig = map[string]any{"model_provider": "routed"}
			if field != "" {
				u.resumeConfig[field] = "explicit"
			}
			if err := u.requestResume(info.ID); err != nil {
				t.Fatal(err)
			}
			read := resumeTestOne(t, w, "thread/read")
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "saved", "turnId": "old", "item": map[string]any{"type": "agentMessage", "id": "answer", "text": "Saved answer"}})
			appServerTestKeys(t, u, "held input\r")
			if w.Len() != 0 {
				t.Fatal("input escaped during settings preflight")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":%q,"path":%q}}}`, read.ID, info.ID, info.Path))
			var request struct {
				Method string `json:"method"`
				Params struct {
					Config   map[string]any `json:"config"`
					Tier     string         `json:"serviceTier"`
					Provider string         `json:"modelProvider"`
				} `json:"params"`
			}
			if err := json.Unmarshal(w.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"model": "saved-model", "model_reasoning_effort": "high", "model_provider": "routed"}
			tier := "priority"
			if field == "service_tier" {
				tier = "explicit"
			} else if field != "" {
				want[field] = "explicit"
			}
			if request.Method != "thread/resume" || !reflect.DeepEqual(request.Params.Config, want) || request.Params.Tier != tier || request.Params.Provider != "routed" {
				t.Fatalf("resume intent: %+v", request)
			}
			wantLaunch := map[string]any{"model_provider": "routed"}
			if field != "" {
				wantLaunch[field] = "explicit"
			}
			if !reflect.DeepEqual(u.resumeConfig, wantLaunch) {
				t.Fatalf("mutated launch overrides: %#v", u.resumeConfig)
			}
		})
	}
}

func TestAppServerResumeSettingsNullIntent(t *testing.T) {
	info := resumeSettingsRollout(t, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"saved-model"}}}`, "")
	u, w := newAppServerTestUI()
	u.thread, u.resumeThread = "", info.ID
	if err := u.requestResume(info.ID); err != nil {
		t.Fatal(err)
	}
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":%q,"path":%q}}}`, read.ID, info.ID, info.Path))
	var request struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(w.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	config := request.Params["config"].(map[string]any)
	if tier, present := request.Params["serviceTier"]; !present || tier != nil {
		t.Fatalf("saved default tier not forwarded: %#v", request.Params)
	}
	if _, present := config["model_reasoning_effort"]; present || !u.resumePendingEffort {
		t.Fatalf("unset effort did not use nullable host settings path: %#v", config)
	}
}

func TestAppServerResumeClearsSavedReasoningDefault(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		t.Run("launch="+effort, func(t *testing.T) {
			u, w := newAppServerTestUI()
			u.thread, u.resumeThread, u.resumePendingEffort = "", "saved", true
			u.agents = newLiveActivityView()
			u.requests["1"] = "thread/resume"
			savedEffort := "null"
			if effort != "" {
				savedEffort = fmt.Sprintf("%q", effort)
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":1,"result":{"model":"saved","reasoningEffort":%q,"collaborationMode":{"mode":"plan","settings":{"model":"saved","reasoning_effort":%s,"developer_instructions":"host instructions"}},"thread":{"id":"saved","cwd":"/workspace"}}}`, effort, savedEffort))
			type request struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			requests := appServerDrainRequests[request](t, w)
			var update *request
			for i := range requests {
				if requests[i].Method == "thread/settings/update" {
					update = &requests[i]
				}
			}
			if effort == "" {
				if update != nil || u.settingsPending {
					t.Fatal("unchanged null reasoning waited for a suppressed event")
				}
				return
			}
			if update == nil || !u.settingsPending {
				t.Fatal("did not clear saved default through Codex")
			}
			wantMode := map[string]any{"mode": "plan", "settings": map[string]any{"model": "saved", "reasoning_effort": nil, "developer_instructions": "host instructions"}}
			if !reflect.DeepEqual(update.Params["collaborationMode"], wantMode) || len(update.Params) != 2 {
				t.Fatalf("default reasoning changed other host settings: %#v", update.Params)
			}
			for _, request := range requests {
				if request.Method != "thread/settings/update" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[],"nextCursor":null}}`, request.ID))
				}
			}
			resumeTestSettle(t, u, w)
			appServerTestKeys(t, u, "held\r")
			if w.Len() != 0 {
				t.Fatal("submitted before reasoning restored")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, update.ID))
			if !u.settingsPending {
				t.Fatal("acknowledgement claimed settings applied")
			}
			appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "saved", "threadSettings": map[string]any{"model": "saved", "effort": nil, "serviceTier": nil}})
			if u.settingsPending || u.reasoningEffort != "" {
				t.Fatal("saved default did not apply")
			}
		})
	}
}

func TestAppServerResumeSettingsPreflightFailure(t *testing.T) {
	for _, result := range []string{`"error":{"code":-1,"message":"missing thread"}`, `"result":{"thread":{"id":"foreign"}}`, `"result":{"thread":{}}`} {
		for _, switching := range []bool{false, true} {
			u, w := newAppServerTestUI()
			if switching {
				u.switching, u.clearing = "saved", true
			} else {
				u.thread, u.resumeThread = "", "saved"
			}
			if err := u.requestResume("saved"); err != nil {
				t.Fatal(err)
			}
			read := resumeTestOne(t, w, "thread/read")
			var m appserver.Message
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":%d,%s}`, read.ID, result)), &m); err != nil {
				t.Fatal(err)
			}
			err := u.message(m)
			if (!switching && err == nil) || (switching && (err != nil || u.thread != "main" || u.clearing)) || w.Len() != 0 {
				t.Fatalf("preflight failure: switching=%v err=%v thread=%q wire=%s", switching, err, u.thread, w.Bytes())
			}
		}
	}
}

func TestAppServerResumeSwitchReasoningIsolation(t *testing.T) {
	for _, result := range []string{"failed", "failed-after-source-update", "resumed-after-source-update"} {
		for _, sourceGate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sourceGate=%v", result, sourceGate), func(t *testing.T) {
				u, w := newResumeSessionTestUI(t)
				u.resumeClearEffort = sourceGate
				info := resumeSettingsRollout(t, `{"type":"turn_context","payload":{"model":"saved","effort":null}}`, "")
				appServerTestKeys(t, u, "/resume saved\r")
				read := resumeTestOne(t, w, "thread/read")
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"saved","path":%q}}}`, read.ID, info.Path))
				resume := resumeTestOne(t, w, "thread/resume")
				if result != "failed" {
					appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "main", "threadSettings": map[string]any{"model": "source", "effort": nil}})
					sourceGate = false // The host cleared the source gate while target resume was pending.
				}
				if result == "resumed-after-source-update" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"saved","reasoningEffort":"high","collaborationMode":{"mode":"default","settings":{"model":"saved","reasoning_effort":"high","developer_instructions":null}},"thread":{"id":"saved","cwd":"/work"}}}`, resume.ID))
					var correction bool
					for _, request := range resumeTestRequests(t, w) {
						correction = correction || request.Method == "thread/settings/update"
					}
					if u.thread != "saved" || !u.resumeClearEffort || !u.settingsPending || !correction {
						t.Fatal("source notification released target default restoration")
					}
					return
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"cannot resume"}}`, resume.ID))
				if u.thread != "main" || u.resumeClearEffort != sourceGate || u.resumePendingEffort {
					t.Fatal("target restoration state leaked into source session")
				}
				appServerTestKeys(t, u, "continue original\r")
				if sourceGate {
					if w.Len() != 0 {
						t.Fatal("failed switch released the original restoration gate")
					}
				} else {
					appServerOneRequest(t, w, "turn/start", "continue original")
				}
			})
		}
	}
}
