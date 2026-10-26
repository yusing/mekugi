package launch

import (
	"strings"
	"testing"
)

func TestAppServerInlineModelSettings(t *testing.T) {
	for _, test := range []struct {
		command string
		change  map[string]any
	}{
		{`/model custom --reasoning=high --speed=fast`, map[string]any{"model": "custom", "effort": "high", "serviceTier": "fast"}},
		{`/model --reasoning high --speed standard`, map[string]any{"effort": "high", "serviceTier": nil}},
		{`/model 'custom model' --speed "flex"`, map[string]any{"model": "custom model", "serviceTier": "flex"}},
	} {
		t.Run(test.command, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Turn, u.model, u.reasoningEffort, u.serviceTier = "active", "old", "low", "priority"
			appServerTestKeys(t, u, test.command+"\r")
			test.change["threadId"] = "main"
			assertAppServerSettingsRequest(t, wire, "thread/settings/update", test.change)
			if u.model != "old" || u.reasoningEffort != "low" || u.serviceTier != "priority" || u.Text != "" || !u.Settings.Pending() {
				t.Fatal("combined intent changed confirmed values or failed to clear its draft")
			}
			appServerTestKeys(t, u, "dependent prompt\r")
			if wire.Len() != 0 || len(u.attached.conversation.Unsent) != 1 {
				t.Fatal("dependent input escaped pending combined settings")
			}
			appServerTestMessage(t, u, `{"id":1,"result":{}}`)
			appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"custom","effort":"high","serviceTier":"priority"}}}`)
			test.change["turnId"] = "active"
			assertAppServerSettingsRequest(t, wire, "turn/settings/update", test.change)
		})
	}
}

func TestAppServerInlineModelInvalidKeepsDraft(t *testing.T) {
	for _, args := range []string{"custom extra", "--reasoning", "--speed=", "--speed fast --speed flex", "--unknown=x", "'unterminated"} {
		u, wire := newAppServerTestUI()
		command := "/model " + args
		appServerTestKeys(t, u, command+"\r")
		if wire.Len() != 0 || u.Text != command || !u.attached.feedback.Alert || u.Settings.Pending() {
			t.Fatalf("%q: draft=%q alert=%v pending=%v wire=%s", command, u.Text, u.attached.feedback.Alert, u.Settings.Pending(), wire.String())
		}
	}
}

func TestAppServerInlineModelSideSpeedKeepsMainScope(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/side\r")
	fork := btwTestRequest(t, wire, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"side"},"model":"test","reasoningEffort":"low"}`)
	appServerTestKeys(t, u, "/model --speed=fast\r")
	if wire.Len() != 0 || u.Text != "/model --speed=fast" || !strings.Contains(u.attached.feedback.Text, "Main") {
		t.Fatal("speed selection escaped its Main-only scope")
	}
}
