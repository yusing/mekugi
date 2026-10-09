package router

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotNativeFaintLabelForeground(t *testing.T) {
	const width = 40
	u := &appServerUI{model: "model", view: newLiveActivityView()}
	frame, _ := u.mainFrame(width, 3, 0)
	rows := []string{
		nativeRule("┌", "┐", "─", nativeTitle(2, "Diff", "", false), activityui.Dim+"metadata"+activityui.Undim, width, nativeBorder(false)),
		nativeRule("┌", "┐", "─", nativeTitle(1, "Main", "", true), "", width, nativeBorder(true)),
		nativeRule("┌", "┐", "─", "\x1b[38;5;170m"+activityui.Dim+"agent"+activityui.Undim, "", width, nativeBorder(false)),
	}
	screen := vt.NewEmulator(width, 1)
	defer screen.Close()
	_, _ = screen.Write([]byte(rows[0]))
	label := screen.CellAt(4, 0).Style
	if label.Fg != nil || label.Attrs&uv.AttrFaint == 0 {
		t.Fatalf("faint label inherited the border foreground or lost faint: %+v", label)
	}
	rows = append(rows, frame...)
	rows = append(rows, "plain after borders")
	uisnapshot.AssertTerminal(t, "testdata/snapshots/native-faint-label-foreground.txt", rows, width)
}

func TestNativeUISubtleForeground(t *testing.T) {
	p := activityui.Painter{}
	u := &appServerUI{model: "x", view: newLiveActivityView()}
	frame, _ := u.mainFrame(40, 3, 0)
	v := newLiveActivityView()
	metrics := nativeRosterColumns([][nativeMetricParts]string{nativeRosterMetricParts(v, activityPaneAgent{Name: "/root/a", ContextKnown: true, ContextTokens: 1000, ContextWindow: 4000, InputTokens: 2000, OutputTokens: 1000}, time.Now())}, 200)[0]
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
				before, _, ok := strings.Cut(text, "agent1")
				if !ok {
					t.Fatal("wait target missing")
				}
				x := ansi.StringWidth(before)
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
