package activity

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Source: internal/router/live_activity_conversation.go:549:560@1426318d conversationHeading.
// EventHeading is shared by transcript and Activity events. Unknown times
// stay absent; a known time is right-aligned only when it fits.
func EventHeading(glyph, name, detail string, observed time.Time, width int) string {
	head := glyph + " " + name
	if detail != "" {
		head += " " + Dim + detail + Undim
	}
	head = ansi.Truncate(head, width, "…")
	if !observed.IsZero() {
		stamp := observed.Local().Format("15:04:05")
		if gap := width - ansi.StringWidth(head) - len(stamp); gap >= 2 {
			head += strings.Repeat(" ", gap) + Dim + stamp + Undim
		}
	}
	return CopyDecoration(head)
}
