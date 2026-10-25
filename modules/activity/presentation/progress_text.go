package presentation

import (
	"strings"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/textsanitize"
)

// Source: modules/activity/terminalview/wait.go:17:40@7166de95.
func (b Value) ProgressText() string {
	var text strings.Builder
	body := b.Body
	if b.Label != "" {
		body = b.Label // Progress disclosures keep the full message in Body.
	}
	text.WriteString(textsanitize.Safe(body, false))
	for i, target := range b.WaitTargets {
		if i == 0 {
			text.WriteString(" ")
		} else {
			text.WriteString(", ")
		}
		name := textsanitize.Safe(pathdisplay.AgentDisplayName(target.Name), false)
		text.WriteString(name)
		if target.Status != "" {
			text.WriteString(": " + textsanitize.Safe(target.Status, false))
		}
	}
	if b.WaitTimeout != "" {
		text.WriteString(" (timeout " + b.WaitTimeout + ")")
	}
	if b.WaitReason != "" {
		text.WriteString(": " + textsanitize.Safe(b.WaitReason, false))
	}
	return text.String()
}
