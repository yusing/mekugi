package router

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func appliedSettingsTestUI(t *testing.T, workspace, thread string) (*appServerUI, *appServerTestInput) {
	t.Helper()
	u := newAppServerSessionTestUI(t, workspace)
	u.thread = thread
	u.session.start(thread, workspace)
	u.panes = new(nativePanePersistence)
	if err := u.panes.open(u.shell, workspace, thread, false); err != nil {
		t.Fatal(err)
	}
	return u, u.client.Input.(*appServerTestInput)
}

func appliedSettingsTestNotify(t *testing.T, u *appServerUI, model, effort, tier string) {
	t.Helper()
	appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": u.thread, "threadSettings": map[string]any{"model": model, "effort": effort, "serviceTier": tier}})
}

func appliedSettingsTestResume(t *testing.T, u *appServerUI, w *appServerTestInput, info appServerThreadInfo) map[string]any {
	t.Helper()
	u.resumeThread = info.ID
	if err := u.requestResume(info.ID); err != nil {
		t.Fatal(err)
	}
	read := resumeTestOne(t, w, "thread/read")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":%q,"cwd":%q,"path":%q}}}`, read.ID, info.ID, info.Cwd, info.Path))
	var request struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal(w.Bytes(), &request); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	if request.Method != "thread/resume" {
		t.Fatalf("expected resume after preflight: %+v", request)
	}
	return request.Params
}

func TestAppServerAppliedSettingsFreshOwnerResumeBeforeFirstTurn(t *testing.T) {
	for _, defaults := range []bool{false, true} {
		t.Run(fmt.Sprint("defaults=", defaults), func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, _ := appliedSettingsTestUI(t, "/workspace", "saved")
			u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
			appliedSettingsTestNotify(t, u, "saved-model", "high", "priority")
			if defaults {
				appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "saved", "threadSettings": map[string]any{"model": "saved-model", "effort": nil, "serviceTier": nil}})
			}
			if u.turn != "" {
				t.Fatal("test unexpectedly started a turn")
			}
			// No live writer, parent, rollout, or previous UI is needed for recovery.
			fresh, w := appliedSettingsTestUI(t, "/workspace", "other")
			fresh.resumeConfig = map[string]any{"model_provider": "routed"}
			params := appliedSettingsTestResume(t, fresh, w, appServerThreadInfo{ID: "saved", Cwd: "/workspace"})
			wantConfig := map[string]any{"model": "saved-model", "model_provider": "routed", "model_reasoning_effort": "high"}
			var wantTier any = "priority"
			if defaults {
				delete(wantConfig, "model_reasoning_effort")
				wantTier = nil
			}
			if !reflect.DeepEqual(params["config"], wantConfig) || params["serviceTier"] != wantTier || fresh.resumePendingEffort != defaults {
				t.Fatalf("recovered params=%#v clearEffort=%v", params, fresh.resumePendingEffort)
			}
		})
	}
}

func TestAppServerAppliedSettingsFlagsOverridePerField(t *testing.T) {
	for _, field := range []string{"model", "model_reasoning_effort", "service_tier"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, _ := appliedSettingsTestUI(t, "/workspace", "saved")
			appliedSettingsTestNotify(t, u, "saved-model", "high", "priority")
			fresh, w := appliedSettingsTestUI(t, "/workspace", "other")
			fresh.resumeConfig = map[string]any{field: "explicit"}
			params := appliedSettingsTestResume(t, fresh, w, appServerThreadInfo{ID: "saved", Cwd: "/workspace"})
			want := map[string]any{"model": "saved-model", "model_reasoning_effort": "high"}
			var tier any = "priority"
			if field == "service_tier" {
				tier = "explicit"
			} else {
				want[field] = "explicit"
			}
			if !reflect.DeepEqual(params["config"], want) || params["serviceTier"] != tier || !reflect.DeepEqual(fresh.resumeConfig, map[string]any{field: "explicit"}) {
				t.Fatalf("per-field precedence or immutable launch flags lost: %#v flags=%#v", params, fresh.resumeConfig)
			}
		})
	}
}

func TestAppServerAppliedSettingsIsolationAndReadKeepsWriter(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u, _ := appliedSettingsTestUI(t, "/workspace", "saved")
	appliedSettingsTestNotify(t, u, "saved-model", "high", "priority")
	live, _ := appliedSettingsTestUI(t, "/workspace", "live")
	appliedSettingsTestNotify(t, live, "live-model", "low", "flex")
	path := live.panes.path
	state, err := live.panes.read("/workspace", "saved")
	if err != nil || state.Settings.Model != "saved-model" || live.panes.path != path || live.panes.settings.Model != "live-model" {
		t.Fatalf("read redirected live writer: state=%+v owner=%+v err=%v", state, live.panes, err)
	}
	appliedSettingsTestNotify(t, live, "live-next", "medium", "")
	state, err = new(nativePanePersistence).read("/workspace", "saved")
	if err != nil || state.Settings.Model != "saved-model" {
		t.Fatalf("live save overwrote another thread: %+v %v", state, err)
	}
	for _, namespace := range [][2]string{{"/different-workspace", "saved"}, {"/workspace", "fork"}, {"/workspace", "side"}} {
		state, err := live.panes.read(namespace[0], namespace[1])
		if err != nil || state.Settings.Model != "" {
			t.Fatalf("namespace leaked settings: %v %+v %v", namespace, state, err)
		}
	}
}

func TestAppServerAppliedSettingsPendingFailedAndForeignDoNotPersist(t *testing.T) {
	for name, message := range map[string]string{
		"pending": `{"id":1,"result":{}}`,
		"foreign": `{"method":"thread/settings/updated","params":{"threadId":"foreign","threadSettings":{"model":"foreign","effort":"high","serviceTier":"priority"}}}`,
		"failed":  `{"id":1,"error":{"code":-1,"message":"rejected"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, w := appliedSettingsTestUI(t, "/workspace", "main")
			appliedSettingsTestNotify(t, u, "original", "low", "flex")
			before, err := os.ReadFile(u.panes.path)
			if err != nil {
				t.Fatal(err)
			}
			appServerTestKeys(t, u, "/tier priority\r")
			w.Reset()
			appServerTestMessage(t, u, message)
			after, err := os.ReadFile(u.panes.path)
			if err != nil || string(before) != string(after) {
				t.Fatalf("non-authoritative event changed retained settings: %s %v", message, err)
			}
			if name == "failed" && u.settings.pending() {
				t.Fatal("rejected request was not settled")
			}
		})
	}
}

func TestAppServerAppliedSettingsStartResultPersists(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u, _ := appliedSettingsTestUI(t, "/workspace", "old")
	u.settings.restoreEffort = true // A new thread must not inherit a failed resume gate.
	u.requests["1"] = "thread/start"
	appServerTestMessage(t, u, `{"id":1,"result":{"model":"started","reasoningEffort":null,"serviceTier":null,"thread":{"id":"new","cwd":"/workspace"}}}`)
	state, err := new(nativePanePersistence).read("/workspace", "new")
	if err != nil || state.Settings.Model != "started" || state.Settings.Effort != "" || state.Settings.Tier != "" || state.Settings.Observed.IsZero() {
		t.Fatalf("host-confirmed initial defaults not retained: %+v %v", state, err)
	}
}

func TestAppServerAppliedSettingsResumeWaitsForDefaultConfirmation(t *testing.T) {
	for _, effort := range []string{"", "high", "medium"} {
		t.Run("replacement="+effort, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, w := appliedSettingsTestUI(t, "/workspace", "saved")
			observed := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			u.clock = func() time.Time { return observed }
			appliedSettingsTestNotify(t, u, "saved-model", "", "")
			before, err := u.panes.read("/workspace", "saved")
			if err != nil {
				t.Fatal(err)
			}
			observed = observed.Add(time.Hour)
			appliedSettingsTestResume(t, u, w, appServerThreadInfo{ID: "saved", Cwd: "/workspace"})
			var resumeID string
			for id, method := range u.requests {
				if method == "thread/resume" {
					resumeID = id
				}
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%s,"result":{"model":"saved-model","reasoningEffort":"high","serviceTier":null,"collaborationMode":{"mode":"default","settings":{"model":"saved-model","reasoning_effort":"high","developer_instructions":null}},"thread":{"id":"saved","cwd":"/workspace"}}}`, resumeID))
			if !u.settings.pending() {
				t.Fatal("did not wait for default restoration")
			}
			after, err := u.panes.read("/workspace", "saved")
			if err != nil || before.Settings != after.Settings || after.ResumeObserved.IsZero() {
				t.Fatalf("resume metadata replaced pending default intent: %v", err)
			}
			var correctionID int
			for _, request := range resumeTestRequests(t, w) {
				if request.Method == "thread/settings/update" {
					correctionID = request.ID
				} else {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[],"nextCursor":null}}`, request.ID))
				}
			}
			resumeTestSettle(t, u, w)
			appServerTestKeys(t, u, "held\r")
			if w.Len() != 0 || correctionID == 0 {
				t.Fatal("input escaped before default confirmation")
			}
			if effort == "" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, correctionID))
				if !u.settings.restoreEffort || w.Len() != 0 {
					t.Fatal("enqueue acknowledgement claimed restoration success")
				}
				appliedSettingsTestNotify(t, u, "saved-model", "", "")
			} else {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, correctionID))
				if u.draft != "held" || !u.settings.restoreEffort || u.settings.pending() || w.Len() != 0 {
					t.Fatal("rejected restoration ran input or lost its recovery draft")
				}
				if err := u.flushInput(); err != nil || w.Len() != 0 {
					t.Fatal("rejected restoration released input on a later event")
				}
				after, err := u.panes.read("/workspace", "saved")
				if err != nil || before.Settings != after.Settings {
					t.Fatal("rejected restoration replaced saved null intent")
				}
				info := resumeSettingsRollout(t, `{"timestamp":"2026-09-30T12:30:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"saved-model","reasoning_effort":"high","service_tier":null}}}`, "")
				info.Cwd = "/workspace"
				fresh, writer := appliedSettingsTestUI(t, "/workspace", "other")
				params := appliedSettingsTestResume(t, fresh, writer, info)
				if !fresh.resumePendingEffort || !reflect.DeepEqual(params["config"], map[string]any{"model": "saved-model"}) {
					t.Fatal("fresh retry promoted transient resume defaults over saved null intent")
				}
				if _, err := u.updateSettings(map[string]any{"effort": effort}); err != nil {
					t.Fatal(err)
				}
				if effort == "medium" {
					appServerOneRequest(t, w, "thread/settings/update", "")
					appliedSettingsTestNotify(t, u, "saved-model", effort, "")
				}
				if w.Len() != 0 || u.draft != "held" {
					t.Fatal("reasoning selection automatically submitted the recovered draft")
				}
				appServerTestKeys(t, u, "\r")
			}
			appServerOneRequest(t, w, "turn/start", "held")
			state, err := new(nativePanePersistence).read("/workspace", "saved")
			if err != nil || state.Settings.Model != "saved-model" || state.Settings.Effort != effort || state.Settings.Tier != "" || !state.ResumeObserved.IsZero() || state.ResumeEvidence.Model != "" || u.settings.pending() || u.settings.restoreEffort {
				t.Fatalf("confirmed defaults not retained: %+v %v", state, err)
			}
		})
	}
}

func TestAppServerAppliedSettingsRejectedRestoreRetainsSelectedEvidence(t *testing.T) {
	for _, source := range []string{"newer-history", "rollout-only", "legacy-no-tier"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			if source == "newer-history" {
				seed, _ := appliedSettingsTestUI(t, "/workspace", "saved")
				seed.clock = func() time.Time { return time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC) }
				appliedSettingsTestNotify(t, seed, "older", "low", "priority")
			}
			record := `{"timestamp":"2026-09-30T12:00:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"selected","reasoning_effort":null,"service_tier":"flex"}}}`
			if source == "legacy-no-tier" {
				record = `{"timestamp":"2026-09-30T12:00:00Z","type":"turn_context","payload":{"model":"selected","effort":null}}`
			}
			info := resumeSettingsRollout(t, record, "")
			info.Cwd = "/workspace"
			u, w := appliedSettingsTestUI(t, "/workspace", "other")
			u.clock = func() time.Time { return time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC) }
			appliedSettingsTestResume(t, u, w, info)
			for id, method := range u.requests {
				if method == "thread/resume" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%s,"result":{"model":"selected","reasoningEffort":"high","serviceTier":"flex","collaborationMode":{"mode":"default","settings":{"model":"selected","reasoning_effort":"high","developer_instructions":null}},"thread":{"id":"saved","cwd":"/workspace"}}}`, id))
					break
				}
			}
			for _, request := range resumeTestRequests(t, w) {
				if request.Method == "thread/settings/update" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, request.ID))
				}
			}
			info = resumeSettingsRollout(t, record, `{"timestamp":"2026-09-30T12:30:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"selected","reasoning_effort":"high","service_tier":"flex"}}}`, "")
			info.Cwd = "/workspace"
			fresh, writer := appliedSettingsTestUI(t, "/workspace", "fresh")
			params := appliedSettingsTestResume(t, fresh, writer, info)
			tier, tierKnown := params["serviceTier"]
			if !fresh.resumePendingEffort || !reflect.DeepEqual(params["config"], map[string]any{"model": "selected"}) || tierKnown != (source != "legacy-no-tier") || tierKnown && tier != "flex" {
				t.Fatalf("fresh retry lost selected evidence: %#v", params)
			}
		})
	}
}

func TestAppServerAppliedSettingsStorageFailurePreservesHostResult(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	u, _ := appliedSettingsTestUI(t, "/workspace", "saved")
	// A file where the existing preference directory belongs makes the real
	// atomic save fail, without permission assumptions or a storage seam.
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(u.panes.path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Dir(u.panes.path), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	appliedSettingsTestNotify(t, u, "applied", "high", "priority")
	if u.model != "applied" || u.reasoningEffort != "high" || u.serviceTier != "priority" || len(u.view.entries) == 0 {
		t.Fatal("storage failure hid the applied host settings or its warning")
	}
}

func TestAppServerAppliedSettingsHostTimestampPrecedence(t *testing.T) {
	for _, hostTime := range []string{"2026-09-30T11:00:00Z", "2026-09-30T13:00:00Z"} {
		t.Run(hostTime, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, _ := appliedSettingsTestUI(t, "/workspace", "saved")
			u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
			appliedSettingsTestNotify(t, u, "retained", "high", "priority")
			info := resumeSettingsRollout(t, fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"host","reasoning_effort":"low","service_tier":"flex"}}}`, hostTime), "")
			info.Cwd = "/workspace"
			fresh, w := appliedSettingsTestUI(t, "/workspace", "other")
			params := appliedSettingsTestResume(t, fresh, w, info)
			model, effort, tier := "retained", "high", "priority"
			if hostTime == "2026-09-30T13:00:00Z" {
				model, effort, tier = "host", "low", "flex"
			}
			if !reflect.DeepEqual(params["config"], map[string]any{"model": model, "model_reasoning_effort": effort}) || params["serviceTier"] != tier {
				t.Fatalf("timestamp precedence lost: %#v", params)
			}
		})
	}
}

func TestAppServerAppliedSettingsMalformedAndLayoutOnly(t *testing.T) {
	for _, body := range []string{"{", `{"version":1,"focus":0,"split":63,"navigatorColumns":20}`} {
		t.Run(body, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			u, _ := appliedSettingsTestUI(t, "/workspace", "saved")
			if err := os.MkdirAll(filepath.Dir(u.panes.path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(u.panes.path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			fresh, w := appliedSettingsTestUI(t, "/workspace", "other")
			params := appliedSettingsTestResume(t, fresh, w, appServerThreadInfo{ID: "saved", Cwd: "/workspace"})
			if !reflect.DeepEqual(params["config"], map[string]any{}) || fresh.panes.settings.Model != "" {
				t.Fatalf("invalid or absent settings applied: %#v", params)
			}
			if body == "{" {
				if len(fresh.view.entries) == 0 {
					t.Fatal("corrupt retention failed invisibly")
				}
			} else {
				if err := fresh.panes.open(fresh.shell, "/workspace", "saved", true); err != nil || fresh.shell.split != 63 || fresh.shell.diff.navigation.Columns != 20 {
					t.Fatalf("layout-only record not backward compatible: %v", err)
				}
			}
		})
	}
}

func TestUISnapshotNativeSettingsPersistenceFailure(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
	u.paneError(errors.New("session preferences storage unavailable"))
	u.view.entries[len(u.view.entries)-1].Observed = now
	rows, _ := u.mainFrame(100, 14, 0)
	assertNativeUISnapshot(t, "native-settings-persistence-failure", rows)
}

func TestUISnapshotNativeResumeReasoningRejected(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.status, u.model, u.reasoningEffort = "Ready", "saved-model", "high"
	u.settings.restoreEffort = true
	u.settings.beginLive()
	u.requests["1"] = "thread/settings/update"
	appServerTestKeys(t, u, "held input\r")
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"host rejected update"}}`)
	rows, _ := u.mainFrame(100, 16, 0)
	assertNativeUISnapshot(t, "native-resume-reasoning-rejected", rows)
}
