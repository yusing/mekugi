package terminalview

import (
	"strings"

	"github.com/yusing/mekugi/libraries/pathdisplay"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

// WaitTarget retains canonical identity for both live and restored wait notices.
type WaitTarget = presentation.WaitTarget

func (b Block) progressText(style string) string {
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
			name = terminalui.DimAgentColor(target.Name) + name + terminalui.Reset + style
		}
		text.WriteString(name)
		if target.Status != "" {
			text.WriteString(": " + textsanitize.Safe(target.Status, false))
		}
	}
	if len(b.WaitTargets) > 2 {
		text.WriteString(" " + Elision{Hidden: len(b.WaitTargets) - 2, Unit: "more", Form: ElisionSuffix, Hovered: b.Hovered}.String() + style)
	}
	if b.WaitTimeout != "" {
		text.WriteString(terminalui.Dim + " (timeout " + textsanitize.Safe(b.WaitTimeout, false) + ")" + terminalui.Reset + style)
	}
	if b.WaitReason != "" {
		text.WriteString(": " + textsanitize.Safe(b.WaitReason, false))
	}
	return text.String()
}
