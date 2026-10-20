// Source: apps/mekugi/internal/router/app_server_input_queue.go:365:419@[4ded0a4e] pending-input preview.
package terminalview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/conversation/presentation"
)

func Pending(view presentation.PendingView, width int) []string {
	var lines []string
	section := func(header string, parts []presentation.PendingText) {
		if len(parts) == 0 {
			return
		}
		lines = append(lines, ansi.Truncate(terminalui.Dim+"• "+terminalui.Undim+header, width, "…")+terminalui.Reset)
		var texts []string
		for _, part := range parts {
			text := part.Text
			if part.Replies > 0 {
				text = fmt.Sprintf("Answering %d question(s)", part.Replies)
			}
			texts = append(texts, text)
		}
		rows := strings.Split(textsanitize.Safe(strings.Join(texts, "\n"), false), "\n")
		for i, row := range rows[:min(3, len(rows))] {
			prefix := "    "
			if i == 0 {
				prefix = "  ↳ "
			}
			lines = append(lines, ansi.Truncate(terminalui.Dim+prefix+row, width, "…")+terminalui.Reset)
		}
		if len(rows) > 3 {
			lines = append(lines, terminalui.Dim+ansi.Truncate("    …", width, "")+terminalui.Reset)
		}
	}
	header := "Waiting to send"
	switch view.Reason {
	case presentation.Steering:
		header = "Steering after the next tool call · esc sends now · ctrl+c interrupts and restores input"
	case presentation.CompactAfterInterrupt:
		header = "Compacting after interruption · ctrl+c cancels compaction"
	case presentation.ShellRunning:
		header = "Waiting for shell command to finish"
	case presentation.SendingAfterInterrupt:
		header = "Sending steer once interrupted · ctrl+c restores input"
	case presentation.RestoringAfterInterrupt:
		header = "Restoring input once interrupted"
	case presentation.CompactRunning:
		header = "Waiting for context compaction"
	case presentation.CompactQueued:
		header = "Compaction queued after this turn · ctrl+c cancels; Main continues"
	case presentation.ApplyingSettings:
		header = "Sending when settings apply"
	}
	section(header, view.Waiting)
	section("Queued for the next turn · alt+↑ edits", view.Queued)
	return lines
}
