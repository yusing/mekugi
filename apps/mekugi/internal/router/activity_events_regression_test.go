package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityRoleGlyph(t *testing.T) {
	v := newLiveActivityView()
	v.Agents = []activityPaneAgent{{Name: "/root/a", Role: "explorer", Responding: true}, {Name: "/root/b", Role: "worker", Responding: true}}
	a, b := v.glyph(v.Agents[0]), v.glyph(v.Agents[1])
	if a == b || ansi.Strip(a) != "◐" || ansi.StringWidth(a) != 1 {
		t.Fatalf("roles lack unique zero-extra-cell styles: %q %q", a, b)
	}
	if v.glyph(v.Agents[0]) != a {
		t.Fatal("role style shifted")
	}
	if strings.Contains(strings.Join(v.metricTable(v.Roster(), time.Now()), ""), "explorer") {
		t.Fatal("role still takes metric space")
	}
}
