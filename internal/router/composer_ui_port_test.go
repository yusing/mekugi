package router

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	terminalui "github.com/yusing/mekugi/internal/ui/terminal"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestSubmittedAttachmentHostSchema(t *testing.T) {
	// Raw Codex UserInput/TextElement wire spelling, not a loopback of input().
	raw := jsontext.Value(`[{"type":"text","text":"$batch-agent-sessions @FIXME.md non-deferred items","text_elements":[{"byteRange":{"start":0,"end":21},"placeholder":"$batch-agent-sessions"},{"byteRange":{"start":22,"end":31},"placeholder":"@FIXME.md"}]}]`)
	text, spans, ok := appServerUserText(raw)
	want := []activityui.TextSpan{{Start: 0, End: 21, Kind: activityui.SkillToken}, {Start: 22, End: 31, Kind: activityui.FileToken}}
	if !ok || !reflect.DeepEqual(spans, want) {
		t.Fatalf("host spans=%+v", spans)
	}
	draft := composerDraft{text: text, skills: []composerSkill{{start: 0, end: 21, name: "batch-agent-sessions"}}, files: []composerFile{{start: 22, end: 31, path: "/work/FIXME.md"}}}
	input := draft.input()
	if _, old := input[0]["textElements"]; old {
		t.Fatal("wrong camelCase enum field")
	}
	if _, ok := input[0]["text_elements"]; !ok {
		t.Fatal("host field absent")
	}
	composer, _ := activityui.LayoutSpans(text, draft.displaySpans(), 80)
	submitted, _ := activityui.LayoutSpans(text, spans, 80)
	if !reflect.DeepEqual(composer, submitted) {
		t.Fatal("submitted tokens differ from composer styles")
	}
	for _, restore := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, t.TempDir())
		item := appServerItem{ID: "input", Type: "userMessage", Content: raw}
		if restore {
			u.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
		} else {
			u.view.applyAppServerItem(u.session.cwd, "main", "main", "t", "input", "item/completed", "", item)
		}
		if !reflect.DeepEqual(u.view.entries[0].native.spans, want) {
			t.Fatalf("restore=%v spans=%+v", restore, u.view.entries[0].native.spans)
		}
	}
}

func TestUISnapshotSubmittedAttachmentTokens(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	raw := jsontext.Value(`[{"type":"text","text":"$batch-agent-sessions @FIXME.md non-deferred items","text_elements":[{"byteRange":{"start":0,"end":21},"placeholder":"$batch-agent-sessions"},{"byteRange":{"start":22,"end":31},"placeholder":"@FIXME.md"}]}]`)
	text, spans, _ := appServerUserText(raw)
	entry := activityPaneEntry{Agent: "You", Text: text, Observed: time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local), native: &liveActivityNativeItem{spans: spans}}
	var out conversationLines
	u.view.userItemContinued(&out, entry, 80, false)
	uisnapshot.Assert(t, "testdata/snapshots/submitted-attachment-tokens.txt", strings.Join(out.lines, "\n")+"\n")
	// Text snapshots strip colors. Verify actual terminal cells share token style.
	screen := vt.NewEmulator(80, 2)
	defer screen.Close()
	fmt.Fprint(screen, strings.Join(out.lines, "\n"))
	first := screen.CellAt(2, 0)
	file := screen.CellAt(24, 0)
	ordinary := screen.CellAt(34, 0)
	if first.Style.Fg == nil || file.Style.Fg == nil || first.Style.Fg == file.Style.Fg || file.Style.Fg == ordinary.Style.Fg {
		t.Fatalf("skill/file/plain styles not distinct: %+v %+v %+v", first.Style, file.Style, ordinary.Style)
	}
}

func pasteTestImage(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image with space.png")
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pngData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestComposerPasteBoundariesAndImages(t *testing.T) {
	image := pasteTestImage(t)
	for _, payload := range []string{"plain\x1b", "plain\x1b[", "plain\x1b]8;;link", "plain\x1b[20", "plain\x02\x03\x16"} {
		u, _ := newAppServerTestUI()
		u.ensureShell()
		t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
		feed := func(text string) {
			t.Helper()
			for _, b := range []byte(text) {
				if err := u.shell.key(b); err != nil {
					t.Fatal(err)
				}
			}
		}
		feed("\x1b[200~" + payload + "\x1b[201~")
		if u.paste || u.shell.paste || u.escape != "" || u.draft != "plain" {
			t.Fatalf("opaque paste=%q states=%v/%v escape=%q", u.draft, u.paste, u.shell.paste, u.escape)
		}
		feed("\x1b[200~'" + image + "'\x1b[201~typed")
		if u.draft != "plain"+"[Image 1] typed" || len(u.images) != 1 || u.paste || u.shell.paste {
			t.Fatalf("next paste stuck: %q %+v", u.draft, u.images)
		}
		feed("\x1b[200~'" + image + "'\x1b[201~")
		if len(u.images) != 2 || !strings.HasSuffix(u.draft, "[Image 2] ") {
			t.Fatalf("consecutive image paste=%q", u.draft)
		}
		if _, err := os.Stat(image); err != nil {
			t.Fatal("user image removed")
		}
	}
}

func TestComposerImagePastePTYLifecycle(t *testing.T) {
	image := pasteTestImage(t)
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	u, _ := newAppServerTestUI()
	u.ensureShell()
	t.Cleanup(func() { u.shell.diff.close(); u.shell.diffScreen.Close() })
	ready := make(chan struct{})
	done := make(chan error, 1)
	// Scrollback must not consume the ESC byte of either paste terminator.
	u.view.following = false
	go func() {
		done <- terminalui.WithRawPane(ctx, slave, slave, "", "", func(keys <-chan byte) error {
			close(ready)
			for {
				select {
				case b, ok := <-keys:
					if !ok {
						return fmt.Errorf("input closed")
					}
					if err := u.shell.key(b); err != nil {
						return err
					}
					if b == '!' && !u.paste {
						if u.shell.paste || len(u.images) != 2 || u.draft != "[Image 1] [Image 2] editable!" {
							return fmt.Errorf("paste lifecycle: %q images=%d state=%v/%v", u.draft, len(u.images), u.paste, u.shell.paste)
						}
						var frame bytes.Buffer
						if err := u.paint(&frame, 80, 20); err != nil {
							return err
						}
						screen := vt.NewEmulator(80, 20)
						defer screen.Close()
						screen.Write(frame.Bytes())
						if !strings.Contains(screen.String(), "[Image 1] [Image 2] editable!") {
							return fmt.Errorf("composer not rendered: %s", screen.String())
						}
						return nil
					}
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	chunks := []string{"\x1b[200~'" + image + "'\x1b[20", "1~", "\x1b[200~'" + image + "'\x1b[201~", "editable!"}
	for _, chunk := range chunks {
		if _, err := master.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if strings.Contains(ansi.Strip(u.draft), "[201~") {
		t.Fatal("boundary leaked")
	}
}
