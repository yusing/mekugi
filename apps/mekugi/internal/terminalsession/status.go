// Source: apps/mekugi/internal/router/session_status_terminal.go:14:87@[ce8700a8].
package terminalsession

import (
	"fmt"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func StatusText(label sessionpresentation.Label) string {
	text := label.Text
	if label.CompletionKnown {
		text += " in " + activityui.FormatDuration(label.Completion)
	}
	if label.FailureKnown {
		text += ": " + label.Failure
	}
	return text
}

// PaintSessionStatus consumes only detached values. Semantic precedence is
// already settled; terminal animation, sanitization and elapsed grammar stay here.
// Source: apps/mekugi/internal/router/app_server_ui.go:1554:1585@1145fbbc.
func PaintSessionStatus(label sessionpresentation.Label, now time.Time, colors terminalui.Colors) string {
	full := StatusText(label)
	text := strings.ReplaceAll(textsanitize.Safe(full, false), "\n", " ")
	if text == "" {
		return ""
	}
	if label.Alert {
		return terminalui.Red + "✗ " + activityui.ErrorPreview(full) + terminalui.Reset
	}
	if label.Running {
		elapsed := time.Duration(0)
		if !label.Started.IsZero() {
			elapsed = max(0, now.Sub(label.Started))
		}
		painted := activityui.StatusPulse(text, now, colors)
		if label.Animation == sessionpresentation.Working {
			painted = activityui.ReasoningShimmer(text, elapsed, colors)
		}
		painted = "\x1b[39m◐ " + painted + terminalui.Reset
		if !label.Started.IsZero() {
			if elapsed := activityui.FormatLiveDuration(elapsed); elapsed != "" {
				painted += terminalui.Dim + " " + elapsed + terminalui.Undim
			}
		}
		return painted
	}
	if label.Animation == sessionpresentation.Pulse {
		return activityui.StatusPulse(text, now, colors) + terminalui.Reset
	}
	return "\x1b[39m" + text + terminalui.Reset
}

// Source: apps/mekugi/internal/router/composer_notice.go:32:91@1145fbbc.
func PaintMainStatus(status sessionpresentation.MainStatus, now time.Time, colors terminalui.Colors) string {
	label := PaintSessionStatus(status.Composer, now, colors)
	if status.ComposerOverride {
		label = status.Composer.Text
	}
	if status.ShellMode {
		label = terminalui.Red + "Shell Mode" + terminalui.Reset + " · " + label
	}
	if status.Processes > 0 {
		if label != "" {
			label += terminalui.Dim + " · " + terminalui.Undim
		}
		label += activityui.StatusPulse(fmt.Sprintf("%d running processes", status.Processes), now, colors) + terminalui.Reset
	}
	notice := PaintSessionStatus(status.Feedback, now, colors)
	if notice == "" {
		return label
	}
	if label == "" {
		return notice
	}
	return label + terminalui.Dim + " · " + terminalui.Undim + notice
}

func MainStatusDetails(status sessionpresentation.MainStatus) string {
	var parts []string
	for _, detail := range status.Details {
		if text := StatusText(detail); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
