package launch

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/terminalui"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestNativeUISubtleForeground(t *testing.T) {
	p := activityui.Painter{}
	v := newActivityAdapter()
	metrics := activityui.RosterColumns([][activityui.MetricParts]string{activityui.RosterMetricParts(v.View, testActivityAgent(sessionpresentation.ActivityAgent{Name: "/root/a", ContextKnown: true, ContextTokens: 1000, ContextWindow: 4000, InputTokens: 2000, OutputTokens: 1000}), time.Now())}, 200)[0]
	for name, row := range map[string]string{
		"roster context": strings.Replace(metrics, "1K/", "x/", 1),
		"roster tokens":  strings.Replace(metrics, "↑2K", "↑x", 1),
		"metadata":       p.Label("Edit", "`file` · x"),
		"path":           terminalui.StyledPath("x/file"),
		"reasoning":      strings.Join(p.Block(activityui.Block{Kind: "summary", Body: "x"}, 80), ""),
		"after link":     strings.Join(p.Block(activityui.Block{Kind: "summary", Body: "before [link](https://example.com) x"}, 80), ""),
	} {
		t.Run(name, func(t *testing.T) {
			screen := vt.NewEmulator(100, 1)
			defer screen.Close()
			_, _ = screen.Write([]byte(terminalui.FaintFallback(row)))
			for x := range 100 {
				cell := screen.CellAt(x, 0)
				if cell.Content != "x" {
					continue
				}
				if cell.Style.Fg == nil {
					t.Fatal("secondary text has no explicit foreground")
				}
				r, g, b, _ := cell.Style.Fg.RGBA()
				if r != 118*257 || g != 118*257 || b != 118*257 {
					t.Fatalf("secondary foreground = %d,%d,%d", r, g, b)
				}
				return
			}
			t.Fatal("secondary text missing")
		})
	}
}

func TestNativeUIFaintOutputPolicy(t *testing.T) {
	for _, faint := range []bool{true, false} {
		t.Run(fmt.Sprint(faint), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "main", "turn": map[string]any{"id": "t"}})
			waitTargetTestAgent(t, u, "a", "/root/agent1", true)
			item := coordinationActivityItems(t)[3]
			item.Arguments = []byte(`{"target":["/root/agent1"]}`)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})

			terminalTest(u).Focus = 3 // Inspect the roster, the sole wait-status surface.
			terminalTest(u).Faint = faint
			var wire bytes.Buffer
			if err := u.testPaint(&wire, 120, 30); err != nil {
				t.Fatal(err)
			}
			screen := vt.NewEmulator(120, 30)
			defer screen.Close()
			screen.Write(wire.Bytes())
			for y := range 30 {
				var row strings.Builder
				for x := range 120 {
					row.WriteString(screen.CellAt(x, y).Content)
				}
				text := row.String()
				if !strings.Contains(text, "Waiting for") {
					continue
				}
				before, _, ok := strings.Cut(text, "agent1")
				if !ok {
					t.Fatal("wait target missing")
				}
				x := ansi.StringWidth(before)
				cell := screen.CellAt(x, y)
				// The terminal theme's mid-tone agent colors stay legible
				// without faint support, so the fallback keeps them.
				want := ansi.IndexedColor(133)
				if cell.Style.Fg != want || (cell.Style.Attrs&uv.AttrFaint != 0) != faint {
					t.Fatalf("target style=%+v, faint=%v", cell.Style, faint)
				}
				return
			}
			t.Fatal("wait notice missing from terminal frame")
		})
	}
}
