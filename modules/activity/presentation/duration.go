package presentation

import (
	"strings"
	"time"
)

// FormatDuration uses the shared compact Activity duration grammar.
func FormatDuration(elapsed time.Duration) string {
	elapsed = max(0, elapsed)
	if elapsed == 0 {
		return "0s"
	}
	var text string
	if elapsed < time.Second {
		text = elapsed.Truncate(time.Millisecond).String()
	} else {
		elapsed = elapsed.Truncate(time.Second)
		text = elapsed.String()
		if elapsed >= time.Minute && elapsed%time.Minute == 0 {
			text = strings.TrimSuffix(text, "0s")
			if elapsed%time.Hour == 0 {
				text = strings.TrimSuffix(text, "0m")
			}
		}
	}
	return text
}
