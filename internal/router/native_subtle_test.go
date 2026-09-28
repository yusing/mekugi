package router

import (
	"bytes"
	"fmt"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestNativeUISubtleForeground(t *testing.T) {
	p := activityui.Painter{}
	u := &appServerUI{model: "x", view: newLiveActivityView()}
	frame, _ := u.mainFrame(40, 3, 0)
	v := newLiveActivityView()
	metrics := nativeRosterMetrics(v, activityPaneAgent{Name: "/root/a", ContextKnown: true, ContextTokens: 1000, ContextWindow: 4000, InputTokens: 2000, OutputTokens: 1000}, time.Now(), 200)
	for name, row := range map[string]string{
		"roster context": strings.Replace(metrics, "1K/", "x/", 1),
		"roster tokens":  strings.Replace(metrics, "↑2K", "↑x", 1),
		"composer":       strings.Join(frame, ""),
		"metadata":       p.Label("Edit", "`file` · x"),
		"path":           activityui.Path("x/file"),
		"reasoning":      strings.Join(p.Block(activityui.Block{Kind: "summary", Body: "x"}, 80), ""),
		"after link":     strings.Join(p.Block(activityui.Block{Kind: "summary", Body: "before [link](https://example.com) x"}, 80), ""),
	} {
		t.Run(name, func(t *testing.T) {
			screen := vt.NewEmulator(100, 1)
			defer screen.Close()
			_, _ = screen.Write([]byte(activityui.FaintFallback(row)))
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
			waitTargetTestEvent(t, u, "item/started", "main", "t", "w")
			u.ensureShell()
			u.shell.focus = 3 // Inspect the roster, the sole wait-status surface.
			u.shell.faint = faint
			var wire bytes.Buffer
			if err := u.paint(&wire, 120, 30); err != nil {
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
				if !strings.Contains(text, "Waiting for agent") {
					continue
				}
				offset := strings.Index(text, "agent1")
				if offset < 0 {
					t.Fatal("wait target missing")
				}
				x := ansi.StringWidth(text[:offset])
				cell := screen.CellAt(x, y)
				want := ansi.IndexedColor(133)
				if faint {
					want = 170
				}
				if cell.Style.Fg != want || (cell.Style.Attrs&uv.AttrFaint != 0) != faint {
					t.Fatalf("target style=%+v, faint=%v", cell.Style, faint)
				}
				return
			}
			t.Fatal("wait notice missing from terminal frame")
		})
	}
}
