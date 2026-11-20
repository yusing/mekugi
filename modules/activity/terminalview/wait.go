package terminalview

import (
	"strings"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// WaitTarget retains canonical identity for both live and restored wait notices.
type WaitTarget = presentation.WaitTarget

func (b Block) progressText(theme diffrender.Theme, style string) string {
	var text strings.Builder
	body := b.Body
	if b.Label != "" {
		body = b.Label // Progress disclosures keep the full message in Body.
	}
	text.WriteString(textsanitize.Safe(body, false))
	for i, target := range b.WaitTargets[:min(2, len(b.WaitTargets))] {
		if i == 0 {
			text.WriteString(" ")
		} else {
			text.WriteString(", ")
		}
		name := textsanitize.Safe(pathdisplay.AgentDisplayName(target.Name), false)
		if style != "" && target.Name != "all" {
			name = terminalui.DimAgentColor(theme, target.Name) + name + terminalui.Reset + style
		}
		text.WriteString(name)
		if target.Status != "" {
			text.WriteString(": " + waitTone(theme, style, textsanitize.Safe(target.Status, false)))
		}
	}
	if len(b.WaitTargets) > 2 {
		text.WriteString(" " + Elision{Hidden: len(b.WaitTargets) - 2, Unit: "more", Form: ElisionSuffix, Hovered: b.Hovered}.String() + style)
	}
	if b.WaitTimeout != "" {
		text.WriteString(terminalui.Dim + " (timeout " + textsanitize.Safe(b.WaitTimeout, false) + ")" + terminalui.Reset + style)
	}
	if reason := b.ProgressReason(); reason != "" {
		// Target outcomes follow a colon; the wait's own reason does not. The
		// no-break space keeps the separator with its reason.
		text.WriteString(" ·\u00a0" + waitTone(theme, style, textsanitize.Safe(reason, false)))
	}
	return text.String()
}

// waitTone keeps unsuccessful wait and target outcomes distinct from completion
// inside a muted progress row: failures in the failure color, and ends without
// an answer in Warning. Unpainted text stays plain.
func waitTone(theme diffrender.Theme, style, outcome string) string {
	color := ""
	switch outcome {
	case "failed", "errored", "delivery failed":
		color = theme.Danger()
	case "timeout", "canceled", "interrupted":
		color = theme.Warning()
	}
	if style == "" || color == "" {
		return outcome
	}
	return terminalui.Reset + color + outcome + terminalui.Reset + style
}
