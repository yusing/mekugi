package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

type runtimeSettingsTestClient struct {
	*runtimeTestClient
	updates []session.Settings
	err     error
}

func (f *runtimeSettingsTestClient) SetSettings(_ context.Context, s session.Settings) error {
	f.updates = append(f.updates, s)
	return f.err
}

func runtimeSettingsTestUI(t *testing.T) (*appServerUI, *runtimeSettingsTestClient) {
	t.Helper()
	u, base := runtimeTestUI(t)
	f := &runtimeSettingsTestClient{runtimeTestClient: base}
	u.runtime.client = f
	if err := u.runtimeEvent(session.Event{Kind: "ready", Models: []session.Model{
		{ID: "sonnet", Resolved: "claude-sonnet", Name: "Sonnet", Description: "Balanced", SupportsEffort: true, Efforts: []string{"low", "high"}},
		{ID: "opus", Resolved: "claude-opus", Name: "Opus", Description: "Complex tasks", SupportsEffort: true, Efforts: []string{"medium", "max"}},
		{ID: "haiku", Resolved: "claude-haiku", Name: "Haiku", Description: "Fast responses", Efforts: []string{"high"}},
	}}); err != nil {
		t.Fatal(err)
	}
	return u, f
}

func runtimeSettingsReceiptForTest(t *testing.T, u *appServerUI, s session.Settings, failed bool) {
	t.Helper()
	if err := u.runtimeEvent(session.Event{Kind: "settings", Settings: &s, Failed: failed, Text: "native rejection"}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRuntimeSettingsPickerDispatch(t *testing.T) {
	for _, tc := range []struct {
		command, field, selected string
		choices                  []string
	}{
		{"/model", "model", "opus", []string{"default", "sonnet", "opus", "haiku"}},
		{"/effort", "effort", "high", []string{"default", "low", "high"}},
		{"/reasoning", "effort", "high", []string{"default", "low", "high"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			u, f := runtimeSettingsTestUI(t)
			runtimeKeys(t, u, tc.command+"\r")
			if !u.picker.open || u.picker.modal != "settings" || u.settingsChoices != tc.command || u.draft != "" {
				t.Fatal("native command did not open the shared settings picker")
			}
			var choices []string
			for _, c := range u.picker.choices {
				choices = append(choices, c.name)
			}
			if !slices.Equal(choices, tc.choices) {
				t.Fatalf("advertised picker choices: %v, want %v", choices, tc.choices)
			}
			u.picker.selected = slices.Index(choices, tc.selected)
			runtimeKeys(t, u, "\r")
			if len(f.updates) != 1 || f.updates[0].Field != tc.field || f.updates[0].Value != tc.selected || f.updates[0].ID == "" || u.picker.open || len(f.sent) != 0 {
				t.Fatalf("picker did not dispatch native control only: updates=%+v input=%q", f.updates, f.sent)
			}
			if u.model != "claude-sonnet" || u.runtime.effortRequest != "" {
				t.Fatal("selection applied before a native receipt")
			}
		})
	}
}

func TestNativeRuntimeSettingsOptionalAndStartup(t *testing.T) {
	for _, command := range []string{"/model", "/model opus", "/effort", "/effort high"} {
		for _, unavailable := range []string{"unsupported", "starting"} {
			t.Run(command+"/"+unavailable, func(t *testing.T) {
				u, f := runtimeSettingsTestUI(t)
				if unavailable == "unsupported" {
					u.runtime.client = f.runtimeTestClient
				} else {
					u.runtime.ready = false
				}
				runtimeKeys(t, u, command+"\r")
				if u.draft != command || u.picker.open || u.runtime.settings != nil || len(f.updates) != 0 || len(f.sent) != 0 {
					t.Fatal("unavailable controls sent input or lost the draft")
				}
			})
		}
	}
}

func TestNativeRuntimeSettingsAdvertisedEfforts(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  []string
	}{
		{"sonnet", []string{"low", "high"}},
		{"claude-sonnet", []string{"low", "high"}},
		{"opus", []string{"medium", "max"}},
		{"claude-opus", []string{"medium", "max"}},
		{"haiku", nil},
		{"claude-haiku", nil},
		{"custom", nil},
	} {
		t.Run(tc.model, func(t *testing.T) {
			u, f := runtimeSettingsTestUI(t)
			u.model = tc.model
			if got := u.effortChoices(); !slices.Equal(got, tc.want) {
				t.Fatalf("effort choices=%v, want %v", got, tc.want)
			}
			runtimeKeys(t, u, "/effort unadvertised\r")
			if len(f.updates) != 0 || len(f.sent) != 0 || u.draft != "/effort unadvertised" {
				t.Fatal("unadvertised effort was dispatched or draft lost")
			}
		})
	}
}

func TestNativeRuntimeSettingsPendingAndBusyAdmission(t *testing.T) {
	u, f := runtimeSettingsTestUI(t)
	runtimeKeys(t, u, "/effort high\r")
	first := f.updates[0]
	runtimeKeys(t, u, "/model opus\r")
	if len(f.updates) != 1 || u.draft != "/model opus" || *u.runtime.settings != first {
		t.Fatal("second settings update escaped pending gate")
	}
	u.loadDraft(composerDraft{})
	runtimeKeys(t, u, "hello\r")
	if len(f.sent) != 0 || u.draft != "hello" {
		t.Fatal("conversation escaped pending settings gate")
	}
	runtimeSettingsReceiptForTest(t, u, first, false)
	runtimeKeys(t, u, "\r")
	if !slices.Equal(f.sent, []string{"hello"}) || !u.runtime.busy {
		t.Fatal("matching receipt did not release conversation input")
	}
	runtimeKeys(t, u, "/model opus\r")
	if len(f.updates) != 2 || f.updates[1].Field != "model" || f.updates[1].ID == first.ID || !u.runtime.busy {
		t.Fatal("busy turn did not admit a distinct settings control")
	}
	runtimeSettingsReceiptForTest(t, u, f.updates[1], false)
	runtimeKeys(t, u, "next input\r")
	if len(f.sent) != 1 || u.draft != "next input" {
		t.Fatal("settings receipt released arbitrary input during active turn")
	}
	if err := u.runtimeEvent(session.Event{Kind: "done"}); err != nil {
		t.Fatal(err)
	}
	runtimeKeys(t, u, "\r")
	if !slices.Equal(f.sent, []string{"hello", "next input"}) {
		t.Fatalf("turn completion did not release preserved input: %q", f.sent)
	}
}

func TestNativeRuntimeSettingsReceiptCorrelationAndRejection(t *testing.T) {
	for _, field := range []string{"model", "effort"} {
		t.Run(field, func(t *testing.T) {
			u, f := runtimeSettingsTestUI(t)
			u.runtime.effortRequest = "low"
			value := "opus"
			if field == "effort" {
				value = "high"
			}
			runtimeKeys(t, u, "/"+field+" "+value+"\r")
			s := f.updates[0]
			for _, mismatched := range []session.Settings{{ID: "other", Field: s.Field, Value: s.Value}, {ID: s.ID, Field: "other", Value: s.Value}, {ID: s.ID, Field: s.Field, Value: "other"}} {
				runtimeSettingsReceiptForTest(t, u, mismatched, false)
				if u.runtime.settings == nil || *u.runtime.settings != s || u.model != "claude-sonnet" || u.runtime.effortRequest != "low" {
					t.Fatal("mismatched receipt changed selections or released pending control")
				}
			}
			if err := u.runtimeEvent(session.Event{Kind: "settings"}); err != nil {
				t.Fatal(err)
			}
			if u.runtime.settings == nil {
				t.Fatal("empty receipt settled the control")
			}
			runtimeSettingsReceiptForTest(t, u, s, true)
			if u.runtime.settings != nil || u.model != "claude-sonnet" || u.runtime.effortRequest != "low" || !u.noticeAlert {
				t.Fatal("native rejection changed the previous selection or left the gate locked")
			}
			runtimeSettingsReceiptForTest(t, u, s, false)
			if u.model != "claude-sonnet" || u.runtime.effortRequest != "low" {
				t.Fatal("late duplicate receipt changed selection")
			}
		})
	}
}

func TestNativeRuntimeSettingsAcceptedIntentAndDefault(t *testing.T) {
	u, f := runtimeSettingsTestUI(t)
	runtimeKeys(t, u, "/model opus\r")
	runtimeSettingsReceiptForTest(t, u, f.updates[0], false)
	if u.model != "opus" || !slices.Equal(u.effortChoices(), []string{"medium", "max"}) {
		t.Fatal("accepted alias did not select advertised model capabilities")
	}
	u.runtimeEvent(session.Event{Kind: "session", SessionID: u.thread, Model: "claude-opus"})
	if u.model != "claude-opus" || !slices.Equal(u.effortChoices(), []string{"medium", "max"}) {
		t.Fatal("native resolved model did not retain advertised capabilities")
	}
	runtimeKeys(t, u, "/effort max\r")
	runtimeSettingsReceiptForTest(t, u, f.updates[1], false)
	if u.runtime.effortRequest != "max" || u.reasoningEffort != "" {
		t.Fatal("accepted effort was not retained as requested-only intent")
	}
	runtimeKeys(t, u, "/effort default\r")
	if u.runtime.effortRequest != "max" || f.updates[2].Value != "default" {
		t.Fatal("default request cleared intent before native acknowledgement")
	}
	runtimeSettingsReceiptForTest(t, u, f.updates[2], false)
	if u.runtime.effortRequest != "" || u.reasoningEffort != "" || len(f.sent) != 0 {
		t.Fatal("default acknowledgement did not clear requested effort only")
	}
}

func TestNativeRuntimeSettingsReasoningStepUsesRequestedIntent(t *testing.T) {
	u, f := runtimeSettingsTestUI(t)
	if err := u.stepReasoning(true); err != nil {
		t.Fatal(err)
	}
	if !u.picker.open || u.settingsChoices != "/effort" || len(f.updates) != 0 {
		t.Fatal("reasoning step without a request assumed an effective effort")
	}
	u.settingsPickerKey("\x1b")
	u.runtime.effortRequest = "low"
	if err := u.stepReasoning(true); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 || f.updates[0].Field != "effort" || f.updates[0].Value != "high" || u.runtime.effortRequest != "low" || len(f.sent) != 0 {
		t.Fatal("reasoning step failed to advance requested intent through native control")
	}
	runtimeSettingsReceiptForTest(t, u, f.updates[0], false)
	if err := u.stepReasoning(true); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 1 {
		t.Fatal("reasoning step advanced beyond advertised efforts")
	}
}

func TestNativeRuntimeSettingsTransportFailureAndTierIsolation(t *testing.T) {
	u, f := runtimeSettingsTestUI(t)
	f.err = errors.New("setter disconnected")
	u.draft = "/model opus"
	handled, err := u.runtimeSettingsCommand(u.draft)
	if !handled || !errors.Is(err, f.err) || u.runtime.settings != nil || u.draft != "/model opus" || u.model != "claude-sonnet" {
		t.Fatal("setter failure lost draft, selection, or locked the control gate")
	}
	f.err = nil
	f.updates = nil
	u.loadDraft(composerDraft{})
	runtimeKeys(t, u, "/tier priority\r")
	if len(f.updates) != 0 || len(f.sent) != 0 || u.draft != "/tier priority" {
		t.Fatal("tier command escaped into native settings or conversation")
	}
	accepted, err := u.updateSettings(map[string]any{"serviceTier": "priority"})
	if err != nil || accepted || len(f.updates) != 0 {
		t.Fatal("shared settings adapter routed tier to native setter")
	}
}

func TestNativeRuntimeSettingsPickerMatchesResolvedModel(t *testing.T) {
	u, _ := runtimeSettingsTestUI(t)
	u.runtime.models = append([]session.Model{{ID: "default", Resolved: "claude-sonnet"}}, u.runtime.models...)
	u.runtimeSettingsCommand("/model")
	if u.picker.choices[u.picker.selected].name != "sonnet" || u.picker.choices[u.picker.selected].description != "Current" {
		t.Fatal("native resolved model did not select its advertised alias")
	}
	if len(u.picker.choices) != 4 {
		t.Fatal("runtime-advertised default row was duplicated")
	}
}

func TestNativeRuntimeSettingsEffortShortcutPreservesDraft(t *testing.T) {
	for _, key := range []string{"\x1b[1;2A", "\x1b[1;2B"} {
		for _, action := range []string{"cancel", "apply", "unsupported", "starting"} {
			t.Run(fmt.Sprintf("%q/%s", key, action), func(t *testing.T) {
				u, f := runtimeSettingsTestUI(t)
				runtimeKeys(t, u, "Keep my ordinary prompt")
				before := u.draftSnapshot()
				if action == "unsupported" {
					u.runtime.client = f.runtimeTestClient
				}
				if action == "starting" {
					u.runtime.ready = false
				}
				runtimeKeys(t, u, key)
				if action == "cancel" {
					runtimeKeys(t, u, "\x03")
				}
				if action == "apply" {
					runtimeKeys(t, u, "\x1b[B\r")
					runtimeSettingsReceiptForTest(t, u, f.updates[0], false)
				}
				if u.draft != before.text || u.cursorBack != before.cursorBack || len(f.sent) != 0 {
					t.Fatal("effort shortcut consumed the ordinary prompt")
				}
			})
		}
	}
}

func TestUISnapshotNativeRuntimeSettings(t *testing.T) {
	for _, width := range []int{42, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, f := runtimeSettingsTestUI(t)
			runtimeKeys(t, u, "/model\r")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-settings-model-%d.txt", width)), runtimeFrame(t, u, width, 28))
			u.settingsPickerKey("\x1b")
			runtimeKeys(t, u, "/effort high\r")
			runtimeSettingsReceiptForTest(t, u, f.updates[0], false)
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-settings-request-%d.txt", width)), runtimeFrame(t, u, width, 28))
			runtimeKeys(t, u, "/effort\r")
			uisnapshot.Assert(t, filepath.Join("testdata", "snapshots", fmt.Sprintf("native-runtime-settings-effort-%d.txt", width)), runtimeFrame(t, u, width, 28))
		})
	}
}
