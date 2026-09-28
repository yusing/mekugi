package activity

import "github.com/yusing/mekugi/internal/livediff"

// WaitTarget retains canonical identity for both live and restored wait notices.
type WaitTarget struct{ Name, Status string }

// ProgressText is the plain-text form used by activity summaries.
func (b Block) ProgressText() string { return b.progressText(false) }

func (b Block) progressText(styled bool) string {
	text := livediff.Safe(b.Body, false)
	for i, target := range b.WaitTargets {
		if i == 0 {
			text += " · "
		} else {
			text += ", "
		}
		name := livediff.Safe(AgentDisplayName(target.Name), false)
		if styled {
			name = DimColor(target.Name) + name + "\x1b[39m" + Dim
		}
		text += name
		if target.Status != "" {
			text += ": " + livediff.Safe(target.Status, false)
		}
	}
	return text
}
