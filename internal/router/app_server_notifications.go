package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/yusing/mekugi/internal/appserver"
)

// Source: codex-rs/tui/src/notifications/{mod,osc9}.rs@1cc7e236
// DesktopNotificationBackend and PostNotification. This is terminal presentation
// only; app-server remains the owner of the turn and request lifecycles.
type nativeNotifications struct {
	out          io.Writer
	focused      bool
	ready        bool
	lastTitle    string
	titleWritten bool
	blocked      map[string]nativeBlockedRequest
	settings     struct {
		Events    jsontext.Value `json:"notifications"`
		Method    string         `json:"notification_method"`
		Condition string         `json:"notification_condition"`
	}
}

type nativeBlockedRequest struct {
	Thread string `json:"threadId"`
	Turn   string `json:"turnId"`
}

func (u *appServerUI) blockNotification(m appserver.Message) {
	n := u.notifications
	if n == nil {
		return
	}
	if _, exists := n.blocked[string(m.ID)]; exists {
		return
	}
	var request nativeBlockedRequest
	_ = json.Unmarshal(m.Params, &request)
	if n.blocked == nil {
		n.blocked = make(map[string]nativeBlockedRequest)
	}
	n.blocked[string(m.ID)] = request
	u.notify("approval-requested", "Approval requested")
}

func (u *appServerUI) resolveNotification(m appserver.Message) {
	if u.notifications == nil || m.Method != "serverRequest/resolved" {
		return
	}
	var p struct {
		ThreadID  string         `json:"threadId"`
		RequestID jsontext.Value `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	if request, ok := u.notifications.blocked[string(p.RequestID)]; ok && (request.Thread == "" || request.Thread == p.ThreadID) {
		delete(u.notifications.blocked, string(p.RequestID))
	}
}

// Herdr's Codex detector consumes the same OSC 0 activity markers as Codex.
// Source: codex-rs/tui/src/chatwidget/status_surfaces.rs:28:43@1cc7e236
// TERMINAL_TITLE_SPINNER_FRAMES and TERMINAL_TITLE_ACTION_REQUIRED_TEXT.
func (u *appServerUI) writeTerminalTitle(now time.Time) {
	n := u.notifications
	if n == nil || n.out == nil {
		return
	}
	parts := []string{"Mekugi"}
	if u.questionCount() > 0 || len(n.blocked) > 0 || len(u.approvals.pending) > 0 {
		parts = append(parts, "[ ! ] Action Required")
	} else if u.turn != "" || u.starting() || u.reset.active() {
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		parts = append(parts, string(frames[(now.UnixMilli()/100)%int64(len(frames))]))
	}
	if u.session.cwd != "" {
		parts = append(parts, filepath.Base(u.session.cwd))
	}
	title := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.Join(parts, " "))
	if title != n.lastTitle {
		u.writeTerminalSignal("\x1b]0;" + title + "\a")
		n.lastTitle, n.titleWritten = title, true
	}
}

func (u *appServerUI) clearTerminalTitle() {
	if n := u.notifications; n != nil && n.titleWritten {
		u.writeTerminalSignal("\x1b]0;\a")
		n.lastTitle, n.titleWritten = "", false
	}
}

func (u *appServerUI) notificationConfig(m appserver.Message) {
	if u.notifications == nil {
		return
	}
	var result struct {
		Config struct {
			TUI jsontext.Value `json:"tui"`
		} `json:"config"`
	}
	if m.Error != nil {
		u.setNotice("Terminal notification settings could not be read", true)
		return
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		u.setNotice("Terminal notification settings could not be read", true)
		return
	}
	if len(result.Config.TUI) > 0 {
		if err := json.Unmarshal(result.Config.TUI, &u.notifications.settings); err != nil {
			u.setNotice("Terminal notification settings could not be read", true)
			return
		}
	}
	u.notifications.ready = true
}

func (n *nativeNotifications) allows(event string) bool {
	if n == nil || n.out == nil || !n.ready || n.focused && n.settings.Condition != "always" {
		return false
	}
	if len(n.settings.Events) == 0 {
		return true
	}
	if n.settings.Events.Kind() == '[' {
		var events []string
		return json.Unmarshal(n.settings.Events, &events) == nil && slices.Contains(events, event)
	}
	var enabled bool
	return json.Unmarshal(n.settings.Events, &enabled) == nil && enabled
}

func (u *appServerUI) notify(event, message string) {
	n := u.notifications
	if !n.allows(event) {
		return
	}
	method := n.settings.Method
	if method == "" || method == "auto" {
		method = "bel"
		switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
		case "ghostty", "iterm.app", "kitty", "warpterminal", "wezterm":
			method = "osc9"
		}
		if os.Getenv("TERM") == "xterm-kitty" || os.Getenv("HERDR_ENV") == "1" {
			method = "osc9"
		}
	}
	sequence := "\a"
	if method == "osc9" {
		sequence = "\x1b]9;" + message + "\a"
		if os.Getenv("TMUX") != "" && os.Getenv("HERDR_ENV") != "1" {
			sequence = "\x1bPtmux;" + strings.ReplaceAll(sequence, "\x1b", "\x1b\x1b") + "\x1b\\"
		}
	}
	u.writeTerminalSignal(sequence)
}

func (u *appServerUI) writeTerminalSignal(sequence string) {
	n := u.notifications
	if n == nil || n.out == nil {
		return
	}
	if _, err := io.WriteString(n.out, sequence); err != nil {
		// Auxiliary notification failure never interrupts host execution.
		n.out = nil
		u.setNotice("Terminal notifications disabled after an output error", true)
	}
}
