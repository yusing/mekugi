package sessionpresentation_test

import (
	"errors"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestControlResponseRestorationWithoutRenderer(t *testing.T) {
	for _, tc := range []struct {
		method, turn, draft, status string
	}{
		{"thread/compact/start", "", "/compact\nwaiting\nqueued", "Ready"},
		{"compact/interrupt", "active", "waiting\nqueued", "Working"},
		{"steer/interrupt", "", "waiting\nqueued", "Interrupting…"},
		{"steer/interrupt", "active", "", "Interrupting…"},
		{"turn/interrupt", "active", "", "turn/interrupt: rejected"},
	} {
		t.Run(tc.method+tc.turn, func(t *testing.T) {
			control := sessioncontrol.Live{Thread: "main", Turn: tc.turn, SteerInterruptAckPending: true, SendSteersAfterInterrupt: true}
			c := &conversationmodel.Session{Unsent: []conversationmodel.Input{{Text: "waiting"}}, Queued: []conversationmodel.Input{{Text: "queued"}}}
			m := new(sessionpresentation.Model)
			m.Status.SetText("Interrupting…")
			var effects sessionpresentation.InputEffects
			if tc.method == "thread/compact/start" {
				effects = m.AcknowledgeCompaction(&control, c, "rejected", true)
			} else {
				effects = m.AcknowledgeInterrupt(&control, c, tc.method, "rejected", true)
			}
			if c.Editor.Text != tc.draft || m.Status.Text != tc.status {
				t.Fatalf("draft=%q status=%q", c.Editor.Text, m.Status.Text)
			}
			if tc.draft != "" && (!effects.Restored.Changed || len(c.Unsent)+len(c.Queued) != 0) {
				t.Fatalf("waiting input not consumed: %+v", effects)
			}
			if tc.method != "turn/interrupt" && (!effects.NoticeError || effects.Notice == "") {
				t.Fatalf("missing failure feedback: %+v", effects)
			}
		})
	}
}

func TestSettingsDispatchAndResponseWithoutRenderer(t *testing.T) {
	control := sessioncontrol.Live{Thread: "main", Turn: "captured"}
	transport := &inputTransport{err: errors.New("unavailable")}
	requests := sessioncontrol.NewRequests[struct{}](transport)
	plan := control.PrepareSettings(preferences.Applied{}, map[string]any{"effort": "high"}, false)
	if err := requests.ApplySettings(&control, plan); err == nil || control.Settings.Pending() || requests.Pending() {
		t.Fatal("failed dispatch admitted settings")
	}
	transport.err = nil
	if err := requests.ApplySettings(&control, plan); err != nil {
		t.Fatal(err)
	}
	response, found := requests.Take("2")
	if !found || response.Scope != "main" || transport.params["threadId"] != "main" {
		t.Fatal("settings bypassed Main correlation")
	}
	if _, err := control.Settings.Acknowledge(response.Method, nil, false); err != nil || !control.Settings.Pending() {
		t.Fatal("enqueue acknowledgement released settings")
	}
	confirmation := control.Settings.Confirmed(control.Turn, "high")
	if !confirmation.Requested || confirmation.Update.Turn != "captured" {
		t.Fatalf("confirmation lost original turn: %+v", confirmation)
	}
	if err := requests.ApplySettings(&control, confirmation.Update); err != nil || transport.method != "turn/settings/update" || transport.params["turnId"] != "captured" {
		t.Fatal("confirmation did not dispatch captured live update")
	}
	response, _ = requests.Take("3")
	if _, err := control.Settings.Acknowledge(response.Method, []byte(`{`), false); err == nil || !control.Settings.Pending() {
		t.Fatal("malformed response settled live update")
	}
	ack, err := control.Settings.Acknowledge(response.Method, []byte(`{"status":"targetUnavailable"}`), false)
	if err != nil || control.Settings.Pending() {
		t.Fatal("live response did not settle settings")
	}
	m := new(sessionpresentation.Model)
	c := &conversationmodel.Session{Unsent: []conversationmodel.Input{{Text: "waiting"}}}
	effects := m.SettingsAcknowledged(c, response.Method, "", ack)
	if effects.Notice != "Settings saved for next turn · live target finished" || effects.NoticeError || len(c.Unsent) != 1 {
		t.Fatalf("finished target feedback/restoration: %+v", effects)
	}
	control.Settings.RestoreEffort = true
	ack, err = control.Settings.Acknowledge("thread/settings/update", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	effects = m.SettingsAcknowledged(c, "thread/settings/update", "rejected", ack)
	if c.Editor.Text != "waiting" || !effects.Restored.Changed || !effects.NoticeError || !control.Settings.BlocksInput() {
		t.Fatalf("failed repair lost draft or independent gate: %+v", effects)
	}
}
