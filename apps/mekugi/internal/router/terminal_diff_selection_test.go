package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/diffmodel"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestTerminalUIDiffSelectionUsesLastPaintedEvidence(t *testing.T) {
	for _, beforePaint := range []string{"retained update", "file navigation", "scroll navigation"} {
		t.Run(beforePaint, func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.shell.saved.store = store
			u.shell.diff.SetWorkspace(workspace)
			u.shell.DiffOpen, u.shell.Focus = true, 1
			scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}}
			refresh := func() {
				t.Helper()
				u.shell.applyDiff(t.Context(), reviewEvent{Kind: "scope", Scope: &scope, Resync: true})
				if u.shell.diffFailure != "" {
					t.Fatal(u.shell.diffFailure)
				}
			}
			publish := func(name string) {
				t.Helper()
				id, err := store.changes.Reserve(t.Context(), store.scoped(t.Context()).changeScope(), workspace, "main", name)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(workspace, name+".go")
				history := mekugiHistory{ChangeID: id, CorrelationID: name, ExecutingThread: "main", Caller: "/root", ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(path, path, "", liveDiffLinesFile(name, 80))}}
				if err := store.put(t.Context(), workspace, map[string]mekugiHistory{name: history}); err != nil {
					t.Fatal(err)
				}
			}
			keys := func(input string) {
				t.Helper()
				for _, key := range []byte(input) {
					if err := u.shell.key(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			screen := vt.NewEmulator(160, 32)
			defer screen.Close()
			paint := func() {
				t.Helper()
				if err := u.paint(screen, 160, 32); err != nil {
					t.Fatal(err)
				}
			}
			if beforePaint != "retained update" {
				publish("a")
				publish("b")
			}
			refresh()
			paint()
			if beforePaint == "retained update" {
				publish("a")
				refresh()
				r := u.shell.layout.Diff
				keys(fmt.Sprintf("\x1b[<0;%d;%dM", r.X+r.W-2, r.Y+2))
				if s := u.shell.selection; s == nil {
					t.Fatal("visible empty Diff did not accept the pointer press")
				} else {
					for _, source := range s.diff {
						if source.Path != "" {
							t.Fatalf("unpainted evidence entered the selection: %+v", source)
						}
					}
					if strings.Contains(strings.Join(s.Rows, "\n"), "a_line_") {
						t.Fatal("unpainted document replaced the visible empty Diff")
					}
				}
				return
			}
			keys("g" + strings.Repeat("j", 8))
			paint()
			r := u.shell.layout.Diff
			x, y, text := -1, -1, ""
			for row := r.Y; row < r.Y+r.H; row++ {
				line := ansi.Strip(u.shell.frame.Rows[row])
				if at := strings.Index(line, "a_line_"); at >= r.X {
					x, y, text = ansi.StringWidth(line[:at]), row, line[at:at+len("a_line_00")]
					break
				}
			}
			if x < 0 {
				t.Fatalf("scrolled a.go source was not painted:\n%s", screen.String())
			}
			if beforePaint == "file navigation" {
				keys("n")
			} else {
				keys("j")
			}
			// Press and drag through the shared decoder before another paint tick.
			keys(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<32;%d;%dM\x1b[<0;%d;%dm", x+1, y+1, x+len(text), y+1, x+len(text), y+1))
			if s := u.shell.selection; s == nil || !strings.Contains(s.Text(), text) {
				t.Fatalf("selection no longer copies the displayed %q: %+v", text, s)
			} else {
				var line int
				if _, err := fmt.Sscanf(text, "a_line_%d", &line); err != nil {
					t.Fatal(err)
				}
				description, path := s.mentionDescription()
				if path != "a.go" || !strings.HasSuffix(description, fmt.Sprintf(":%d", line+1)) {
					t.Fatalf("selection changed displayed source identity: %q %q, want a.go:%d", description, path, line+1)
				}
			}
		})
	}
}
