package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// Source: codex-rs/tui/src/notifications/{mod,osc9}.rs@1cc7e236
// DesktopNotificationBackend and PostNotification. This is terminal presentation
// only; app-server remains the owner of the turn and request lifecycles.
type nativeNotifications struct {
	out          io.Writer
	focused      bool
	ready        bool
	relay        func(string, string)
	lastTitle    string
	titleWritten bool
	lastStatus   string
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

func (n *nativeNotifications) blockNotification(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, m appserver.Message) {
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
	n.notify(feedback, terminal, clock, "approval-requested", "Approval requested")
}

func (n *nativeNotifications) resolve(m appserver.Message) {
	if n == nil || m.Method != "serverRequest/resolved" {
		return
	}
	var p struct {
		ThreadID  string         `json:"threadId"`
		RequestID jsontext.Value `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	if request, ok := n.blocked[string(p.RequestID)]; ok && (request.Thread == "" || request.Thread == p.ThreadID) {
		delete(n.blocked, string(p.RequestID))
	}
}

func (n *nativeNotifications) writeTitle(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, workspace string, actionRequired, busy bool, now time.Time) {
	if n == nil || n.out == nil {
		return
	}
	parts := []string{"Mekugi"}
	if actionRequired {
		parts = append(parts, "[ ! ] Action Required")
	} else if busy {
		frames := []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
		parts = append(parts, string(frames[(now.UnixMilli()/100)%int64(len(frames))]))
	}
	if workspace != "" {
		parts = append(parts, filepath.Base(workspace))
	}
	title := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.Join(parts, " "))
	if title != n.lastTitle {
		n.writeTerminalSignal(feedback, terminal, clock, "\x1b]0;"+title+"\a")
		n.lastTitle, n.titleWritten = title, true
	}
}

func (n *nativeNotifications) clearTerminalTitle(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time) {
	if n != nil && n.titleWritten {
		n.writeTerminalSignal(feedback, terminal, clock, "\x1b]0;\a")
		n.lastTitle, n.titleWritten = "", false
	}
}

// OSC 7501 describes the foreground Mekugi client, independently of desktop
// notification policy and terminal-specific environment hints.
func (n *nativeNotifications) writeStatus(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, actionRequired, busy bool) {
	if n == nil || n.out == nil {
		return
	}
	state := "idle"
	if actionRequired {
		state = "blocked"
	} else if busy {
		state = "working"
	}
	if state != n.lastStatus {
		n.writeTerminalSignal(feedback, terminal, nil, "\x1b]7501;state="+state+":app=mekugi\x1b\\")
		n.lastStatus = state
	}
}

func (n *nativeNotifications) clearTerminalStatus(feedback *noticepresentation.Model, terminal *terminalsession.Attachment) {
	n.clearTerminalTitle(feedback, terminal, nil)
	if n != nil && n.lastStatus != "" {
		n.writeTerminalSignal(feedback, terminal, nil, "\x1b]7501;state=clear\x1b\\")
		n.lastStatus = ""
	}
}

func (n *nativeNotifications) notificationConfig(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, m appserver.Message) {
	if n == nil {
		return
	}
	var result struct {
		Config struct {
			TUI jsontext.Value `json:"tui"`
		} `json:"config"`
	}
	if m.Error != nil {
		setNotice(feedback, terminal, presentationTime(clock), "Terminal notification settings could not be read", true)
		return
	}
	if err := json.Unmarshal(m.Result, &result); err != nil {
		setNotice(feedback, terminal, presentationTime(clock), "Terminal notification settings could not be read", true)
		return
	}
	if len(result.Config.TUI) > 0 {
		if err := json.Unmarshal(result.Config.TUI, &n.settings); err != nil {
			setNotice(feedback, terminal, presentationTime(clock), "Terminal notification settings could not be read", true)
			return
		}
	}
	n.ready = true
}

func (n *nativeNotifications) allows(event string) bool {
	if n == nil || n.out == nil || !n.ready {
		return false
	}
	policy := noticepresentation.DesktopPolicy{Enabled: true, Always: n.settings.Condition == "always"}
	if n.settings.Events.Kind() == '[' {
		if json.Unmarshal(n.settings.Events, &policy.Events) != nil {
			return false
		}
	} else if len(n.settings.Events) != 0 {
		if json.Unmarshal(n.settings.Events, &policy.Enabled) != nil {
			return false
		}
	}
	return policy.Allows(event, n.focused)
}

func (n *nativeNotifications) notify(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, event, message string) {
	if n != nil && n.relay != nil {
		n.relay(event, message)
		return
	}
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
	n.writeTerminalSignal(feedback, terminal, clock, sequence)
}

func (n *nativeNotifications) writeTerminalSignal(feedback *noticepresentation.Model, terminal *terminalsession.Attachment, clock func() time.Time, sequence string) {
	if n == nil || n.out == nil {
		return
	}
	if _, err := io.WriteString(n.out, sequence); err != nil {
		// Auxiliary notification failure never interrupts host execution.
		n.out = nil
		setNotice(feedback, terminal, presentationTime(clock), "Terminal notifications disabled after an output error", true)
	}
}
