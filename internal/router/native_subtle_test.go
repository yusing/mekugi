package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestNativeUISubtleForeground(t *testing.T) {
	p := liveActivityPainter{}
	u := &appServerUI{model: "x", view: newLiveActivityView()}
	frame, _ := u.mainFrame(40, 3, 0)
	for name, row := range map[string]string{
		"composer":   strings.Join(frame, ""),
		"metadata":   p.label("Edit", "`file` · x"),
		"path":       liveActivityPath("x/file"),
		"reasoning":  strings.Join(p.block(liveActivityBlock{kind: "summary", body: "x"}, 80), ""),
		"after link": strings.Join(p.block(liveActivityBlock{kind: "summary", body: "before [link](https://example.com) x"}, 80), ""),
	} {
		t.Run(name, func(t *testing.T) {
			screen := vt.NewEmulator(100, 1)
			defer screen.Close()
			_, _ = screen.Write([]byte(row))
			for x := range 100 {
				cell := screen.CellAt(x, 0)
				if cell.Content != "x" {
					continue
				}
				if cell.Style.Fg == nil {
					t.Fatal("secondary text has no explicit foreground")
				}
				r, g, b, _ := cell.Style.Fg.RGBA()
				if r != 115*257 || g != 115*257 || b != 116*257 {
					t.Fatalf("secondary foreground = %d,%d,%d", r, g, b)
				}
				return
			}
			t.Fatal("secondary text missing")
		})
	}
}
