package diffrender

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffmodel"
)

func TestFileLabelPathSuffixes(t *testing.T) {
	status := diffmodel.Status{Before: "/w/a-long-directory/before.go", After: "/w/a-long-directory/distinctive.go", Edited: true}
	for _, width := range []int{7, 18, 80} {
		got := ansi.Strip(FileLabel(status, "a-long-directory/distinctive.go", "/w", DarkTheme, width))
		if !strings.HasPrefix(got, "RM ") || !strings.HasSuffix(got, ".go") || ansi.StringWidth(got) > width {
			t.Fatalf("rename at width %d lost status or destination suffix: %q", width, got)
		}
		if width >= 18 && (!strings.Contains(got, "→") || !strings.Contains(got, "e.go")) {
			t.Fatalf("rename lost source: %q", got)
		}
	}
	if status.After != "/w/a-long-directory/distinctive.go" || status.Before != "/w/a-long-directory/before.go" {
		t.Fatal("painting changed rename endpoints")
	}
}
