package sessioncontrol_test

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestSettingsPrepareCommitAndCapturedTarget(t *testing.T) {
	h := sessioncontrol.Live{Thread: "main", Turn: "old"}
	change := map[string]any{"effort": "high"}
	plan := h.PrepareSettings(preferences.Applied{Effort: "low"}, change, false)
	if plan.Action != sessioncontrol.SettingsThread || plan.Turn != "old" || h.Settings.Pending() {
		t.Fatal("preparation issued an operation or lost its target")
	}
	change["effort"] = "changed caller input"
	h.Settings.Admit(plan)
	result := h.Settings.Confirmed("new", "high")
	if !result.Requested || result.Update.Action != sessioncontrol.SettingsUnchanged || result.Update.Change["effort"] != "high" || h.Settings.Pending() {
		t.Fatalf("confirmation retargeted input or borrowed caller mutation: %+v", result)
	}
	h.Turn = ""
	h.Settings.RestoreEffort = true
	plan = h.PrepareSettings(preferences.Applied{}, map[string]any{"effort": nil}, false)
	if plan.Action != sessioncontrol.SettingsUnchanged || !plan.RestoreDefault || !h.Settings.RestoreEffort {
		t.Fatal("no-op preparation changed the independent restoration gate")
	}
	h.Settings.Admit(plan)
	if h.Settings.BlocksInput() {
		t.Fatal("admitted default restoration still blocks input")
	}
}
