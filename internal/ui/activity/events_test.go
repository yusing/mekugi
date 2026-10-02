package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestEventHeadingTimestampAvailability(t *testing.T) {
	stamp := time.Date(2026, 10, 2, 12, 34, 56, 0, time.Local)
	for _, tc := range []struct {
		width int
		at    time.Time
		stamp bool
	}{
		{40, stamp, true},
		{12, stamp, false},
		{40, time.Time{}, false},
	} {
		row := ansi.Strip(EventHeading("◆", "journal", "", tc.at, tc.width))
		if strings.Contains(row, "12:34:56") != tc.stamp || strings.Contains(row, "00:00:00") || ansi.StringWidth(row) > tc.width {
			t.Fatalf("invalid timestamp or heading width: %q", row)
		}
	}
}

func TestErrorHasDetails(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"failed", false},
		{"\n  failed\n\n", false},
		{"\x1b[31mfailed\x1b[0m", false},
		{"failed\nMore information", true},
		{strings.Repeat("x", 241), true},
	} {
		if got := ErrorHasDetails(Block{Body: tc.body}); got != tc.want {
			t.Errorf("details=%t for %q", got, tc.body)
		}
	}
}
