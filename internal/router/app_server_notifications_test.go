package router

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func notificationTestOutput(t *testing.T, u *appServerUI) *bytes.Buffer {
	t.Helper()
	for _, key := range []string{"TERM_PROGRAM", "TERM", "TMUX", "HERDR_ENV"} {
		t.Setenv(key, "")
	}
	out := new(bytes.Buffer)
	u.notifications = &nativeNotifications{out: out, ready: true}
	u.notifications.settings.Method = "osc9"
	return out
}

func TestAppServerNotificationMethods(t *testing.T) {
	for _, tc := range []struct{ name, method, program, term, tmux, herdr, want string }{
		{name: "OSC9", method: "osc9", want: "\x1b]9;Agent turn complete\a"},
		{name: "BEL", method: "bel", program: "kitty", want: "\a"},
		{name: "auto fallback", method: "auto", want: "\a"},
		{name: "default fallback", want: "\a"},
		{name: "auto terminal", method: "auto", program: "WezTerm", want: "\x1b]9;Agent turn complete\a"},
		{name: "auto kitty TERM", method: "auto", term: "xterm-kitty", want: "\x1b]9;Agent turn complete\a"},
		{name: "auto herdr", method: "auto", herdr: "1", want: "\x1b]9;Agent turn complete\a"},
		{name: "tmux", method: "osc9", tmux: "/tmp/tmux", want: "\x1bPtmux;\x1b\x1b]9;Agent turn complete\a\x1b\\"},
		{name: "herdr inside tmux", method: "auto", tmux: "/tmp/tmux", herdr: "1", want: "\x1b]9;Agent turn complete\a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			out := notificationTestOutput(t, u)
			u.notifications.settings.Method = tc.method
			t.Setenv("TERM_PROGRAM", tc.program)
			t.Setenv("TERM", tc.term)
			t.Setenv("TMUX", tc.tmux)
			t.Setenv("HERDR_ENV", tc.herdr)
			u.notify("agent-turn-complete", "Agent turn complete")
			if out.String() != tc.want {
				t.Fatalf("notification = %q, want %q", out.String(), tc.want)
			}
		})
	}
}

func TestAppServerNotificationConfig(t *testing.T) {
	for _, tc := range []struct {
		name, tui, event string
		focused, want    bool
	}{
		{name: "defaults", tui: `{}`, event: "agent-turn-complete", want: true},
		{name: "disabled", tui: `{"notifications":false}`, event: "agent-turn-complete"},
		{name: "enabled", tui: `{"notifications":true}`, event: "agent-turn-complete", want: true},
		{name: "allowlist match", tui: `{"notifications":["async-question"]}`, event: "async-question", want: true},
		{name: "allowlist excludes", tui: `{"notifications":["async-question"]}`, event: "agent-turn-complete"},
		{name: "empty allowlist", tui: `{"notifications":[]}`, event: "async-question"},
		{name: "focused", tui: `{}`, event: "agent-turn-complete", focused: true},
		{name: "always", tui: `{"notification_condition":"always"}`, event: "agent-turn-complete", focused: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			out := notificationTestOutput(t, u)
			u.notifications.ready = false
			u.notifications.focused = tc.focused
			u.notify(tc.event, "test")
			if out.Len() != 0 {
				t.Fatal("notification before configuration")
			}
			u.requests["42"] = "config/read"
			appServerTestMessage(t, u, `{"id":42,"result":{"config":{"tui":`+tc.tui+`}}}`)
			if !u.notifications.ready || u.requests["42"] != "" {
				t.Fatal("configuration response was not consumed")
			}
			u.notify(tc.event, "test")
			if (out.Len() > 0) != tc.want {
				t.Fatalf("notification = %q, want emitted %v", out.String(), tc.want)
			}
		})
	}
	for _, reply := range []string{`{"id":42,"error":{"code":-1,"message":"unavailable"}}`, `{"id":42,"result":{"config":{"tui":42}}}`} {
		u, _ := newAppServerTestUI()
		out := notificationTestOutput(t, u)
		u.notifications.ready = false
		u.requests["42"] = "config/read"
		appServerTestMessage(t, u, reply)
		u.notify("agent-turn-complete", "test")
		if u.notifications.ready || out.Len() != 0 || u.notice == "" {
			t.Fatal("invalid configuration enabled notifications or hid the error")
		}
	}
}

func TestAppServerNotificationCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, thread, turn, status string
		want                       bool
	}{
		{"main", "main", "turn", "completed", true},
		{"child", "child", "turn", "completed", false},
		{"stale", "main", "old", "completed", false},
		{"failed", "main", "turn", "failed", false},
		{"interrupted", "main", "turn", "interrupted", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			out := notificationTestOutput(t, u)
			appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn"}}}`)
			wire := fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"id":%q,"status":%q}}}`, tc.thread, tc.turn, tc.status)
			appServerTestMessage(t, u, wire)
			appServerTestMessage(t, u, wire)
			want := ""
			if tc.want {
				want = "\x1b]9;Agent turn complete\a"
			}
			if out.String() != want {
				t.Fatalf("completion output = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestAppServerNotificationQuestions(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%v", async), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			out := notificationTestOutput(t, u)
			u.turn = "turn"
			want := "\x1b]9;Input requested\a"
			for range 2 {
				if async {
					questionTestAsync(t, u, "item", "Who receives it?", "Who approves it?")
					want = "\x1b]9;Question pending\a"
				} else {
					questionTestSync(t, u, "request", false)
				}
			}
			if out.String() != want {
				t.Fatalf("question output = %q, want %q", out.String(), want)
			}
			if async {
				appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"completed"}}}`)
				if out.String() != want {
					t.Fatal("pending question also emitted completion")
				}
			}
		})
	}
}

func TestAppServerNotificationHistorySilent(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	out := notificationTestOutput(t, u)
	var turns []appServerHistoryTurn
	if err := json.Unmarshal([]byte(`[{"id":"past","status":"completed","items":[{"id":"answer","type":"agentMessage","text":"Done"}]},{"id":"question-turn","status":"completed","items":[{"id":"question","type":"agentMessage","delivery":"async","phase":"finalAnswer","text":"Who receives it?","questions":[{"title":"Who receives it?","options":["Customers","Internal"]}]}]}]`), &turns); err != nil {
		t.Fatal(err)
	}
	u.restoreHistory(turns)
	if u.questionCount() != 1 {
		t.Fatal("history did not restore pending question")
	}
	if out.Len() != 0 {
		t.Fatalf("history emitted %q", out.String())
	}
}

func TestTerminalUINotificationFocusReports(t *testing.T) {
	for pane := 0; pane < 4; pane++ {
		t.Run(fmt.Sprint(pane), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			notificationTestOutput(t, u)
			u.shell.focus = pane
			send := func(s string) {
				t.Helper()
				for i := range len(s) {
					if err := u.shell.key(s[i]); err != nil {
						t.Fatal(err)
					}
				}
			}
			send("\x1b[I")
			if !u.notifications.focused {
				t.Fatal("focus-in not consumed")
			}
			send("\x1b[O")
			if u.notifications.focused || u.draft != "" || u.shell.focus != pane || u.shell.sequence != "" {
				t.Fatal("focus report leaked into pane input")
			}
			send("\x1b[200~\x1b[I\x1b[201~")
			if u.notifications.focused || u.shell.paste {
				t.Fatal("paste was interpreted as focus report or left open")
			}
			send("\x1b[I")
			if !u.notifications.focused {
				t.Fatal("focus parser did not recover after paste")
			}
		})
	}
}

type notificationFailWriter struct{ calls int }

func (w *notificationFailWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, errors.New("terminal unavailable")
}

func TestAppServerNotificationOutputFailureNonfatal(t *testing.T) {
	u, _ := newAppServerTestUI()
	notificationTestOutput(t, u)
	failing := new(notificationFailWriter)
	u.notifications.out = failing
	appServerTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"main","turn":{"id":"turn"}}}`)
	appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"main","turn":{"id":"turn","status":"completed"}}}`)
	u.notify("agent-turn-complete", "again")
	if u.turn != "" || !strings.HasPrefix(u.status, "Completed") || failing.calls != 1 || u.notice == "" {
		t.Fatalf("output failure disrupted completion: turn=%q status=%q calls=%d notice=%q", u.turn, u.status, failing.calls, u.notice)
	}
}
