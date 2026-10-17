package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityRoleGlyph(t *testing.T) {
	v := newActivityAdapter()
	v.Agents = []sessionpresentation.ActivityAgent{{Name: "/root/a", Role: "explorer", Responding: true}, {Name: "/root/b", Role: "worker", Responding: true}}
	a, b := v.Glyph(v.testAgent(v.Agents[0])), v.Glyph(v.testAgent(v.Agents[1]))
	if a == b || ansi.Strip(a) != "◐" || ansi.StringWidth(a) != 1 {
		t.Fatalf("roles lack unique zero-extra-cell styles: %q %q", a, b)
	}
	if v.Glyph(v.testAgent(v.Agents[0])) != a {
		t.Fatal("role style shifted")
	}
	if strings.Contains(strings.Join(v.MetricTable(v.testRoster(), time.Now()), ""), "explorer") {
		t.Fatal("role still takes metric space")
	}
}
