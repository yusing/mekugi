package activity

import "strings"

import "github.com/yusing/mekugi/internal/livediff"

// WaitTarget retains canonical identity for both live and restored wait notices.
type WaitTarget struct{ Name, Status string }

// ProgressText is the plain-text form used by activity summaries.
func (b Block) ProgressText() string { return b.progressText("") }

func (b Block) progressText(style string) string {
	var text strings.Builder
	body := b.Body
	if b.Label != "" {
		body = b.Label // Progress disclosures keep the full message in Body.
	}
	text.WriteString(livediff.Safe(body, false))
	for i, target := range b.WaitTargets {
		if i == 0 {
			text.WriteString(" · ")
		} else {
			text.WriteString(", ")
		}
		name := livediff.Safe(AgentDisplayName(target.Name), false)
		if style != "" {
			name = DimColor(target.Name) + name + Reset + style
		}
		text.WriteString(name)
		if target.Status != "" {
			text.WriteString(": " + livediff.Safe(target.Status, false))
		}
	}
	return text.String()
}
