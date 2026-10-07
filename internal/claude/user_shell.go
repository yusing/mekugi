package claude

import (
	"html"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

// Native user-shell carriers escape their fields. Parse their outer boundaries
// before decoding once, so literal output cannot become structure.
func shellInput(text string) (string, bool) {
	value, ok := strings.CutPrefix(text, "<bash-input>")
	if !ok {
		return "", false
	}
	value, ok = strings.CutSuffix(value, "</bash-input>")
	return html.UnescapeString(value), ok
}

func shellOutput(text string) (string, *int) {
	value, ok := strings.CutPrefix(text, "<bash-stdout>")
	if !ok {
		return "", nil
	}
	stdout, rest, ok := strings.Cut(value, "</bash-stdout><bash-stderr>")
	if !ok {
		return "", nil
	}
	stderr, rest, ok := strings.Cut(rest, "</bash-stderr><bash-exit-code>")
	if !ok {
		return "", nil
	}
	code, ok := strings.CutSuffix(rest, "</bash-exit-code>")
	if !ok {
		return "", nil
	}
	exit, err := strconv.Atoi(code)
	if err != nil {
		return "", nil
	}
	return html.UnescapeString(stdout) + html.UnescapeString(stderr), &exit
}

func (a *adapter) shellHistory(e nativeEvent) ([]session.Event, bool) {
	if e.Type != "user" || e.Parent != "" {
		return nil, false
	}
	text := contentText(e.Message.Content)
	// Native transcript-only appends can coalesce into one user entry whose
	// UUID belongs to the last append. Restore the pair from that native record.
	if input, output, ok := strings.Cut(text, "</bash-input>\n"); ok {
		command, valid := shellInput(input + "</bash-input>")
		value, code := shellOutput(output)
		if valid && code != nil {
			a.historyShell = nil
			c := session.ShellCommand{ID: "history/" + e.UUID, SessionID: e.SessionID, Command: command}
			return []session.Event{
				{Kind: "shell_started", Shell: &session.ShellResult{ShellCommand: c}, Historical: true},
				{Kind: "shell_done", Shell: &session.ShellResult{ShellCommand: c, Output: value, ExitCode: code, Retained: true}, Historical: true},
			}, true
		}
	}
	if command, ok := shellInput(text); ok {
		a.historyShell = &session.ShellCommand{ID: "history/" + e.UUID, SessionID: e.SessionID, Command: command}
		return []session.Event{{Kind: "shell_started", Shell: &session.ShellResult{ShellCommand: *a.historyShell}, Historical: true}}, true
	}
	if a.historyShell != nil {
		command := *a.historyShell
		a.historyShell = nil
		if output, code := shellOutput(text); code != nil {
			return []session.Event{{Kind: "shell_done", Shell: &session.ShellResult{ShellCommand: command, Output: output, ExitCode: code, Retained: true}, Historical: true}}, true
		}
	}
	return nil, false
}
