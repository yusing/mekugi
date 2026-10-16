package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func TestComposerCommandCatalog(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "" // Local commands do not depend on workspace discovery.
	appServerTestKeys(t, u, "/")
	if !u.picker.open || len(u.picker.choices) != len(nativeCommands) || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("catalog = %+v, requests = %s", u.picker, wire.Bytes())
	}
	frame := ansi.Strip(strings.Join(u.renderPicker(80, 8), "\n"))
	if !strings.Contains(frame, "/model") || !strings.Contains(frame, u.picker.choices[0].description) {
		t.Fatalf("missing command labels/descriptions: %s", frame)
	}
	appServerTestKeys(t, u, "\x1b[B\t")
	if u.Text != "/reasoning " || u.picker.open || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("Tab did not complete locally: draft=%q picker=%+v", u.Text, u.picker)
	}
}

func TestComposerCommandEnterAndDismiss(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/sk")
	if len(u.picker.choices) != 1 || u.picker.choices[0].name != "/skills" {
		t.Fatalf("filtered choices = %+v", u.picker.choices)
	}
	appServerTestKeys(t, u, "\r")
	if u.picker.modal != "menu" || u.Text != "" || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("Enter did not open skills menu: %+v", u.picker)
	}
	u, _ = newAppServerTestUI()
	appServerTestKeys(t, u, "/")
	u.pickerKey("\x1b")
	u.refreshPicker()
	if u.picker.open || u.Text != "/" {
		t.Fatal("Escape must dismiss without changing the draft")
	}
	appServerTestKeys(t, u, "mod")
	if !u.picker.open || len(u.picker.choices) != 1 {
		t.Fatal("editing after dismissal must reopen filtered catalog")
	}
}

func TestComposerCommandBoundaries(t *testing.T) {
	for _, draft := range []string{"hello /", "/model value", "/skills\ntext", "\x1b[200~/skills\x1b[201~"} {
		t.Run(draft, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, draft)
			if u.picker.open {
				t.Fatalf("unexpected catalog for %q", draft)
			}
		})
	}
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/nonexistent\r")
	if !u.feedback.Alert || !strings.Contains(u.feedback.Text, "Unknown command") || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("unknown command leaked or lacked feedback: %q", u.feedback.Text)
	}
}

func TestComposerCommandSmallFrame(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "/\x1b[A")
	for _, width := range []int{1, 12, 40} {
		frame := u.renderPicker(width, 2)
		for _, row := range frame {
			if ansi.StringWidth(row) > width {
				t.Fatalf("row exceeds width %d: %q", width, row)
			}
		}
		if u.picker.top > u.picker.selected || u.picker.top+u.picker.rowCount <= u.picker.selected {
			t.Fatal("selection outside visible viewport")
		}
	}
}

func TestComposerCommandMouseSelection(t *testing.T) {
	u, wire := newAppServerTestUI()
	shell := &terminalUI{main: u}
	u.shell = shell
	appServerTestKeys(t, u, "/sk")
	u.renderPicker(80, 4)
	u.picker.rect = terminalsession.Rect{0, 0, 80, 4}
	if err := shell.mouse("\x1b[<0;2;2M"); err != nil {
		t.Fatal(err)
	}
	if u.picker.modal != "menu" || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("click did not dispatch local command: %+v", u.picker)
	}
}

func TestUISnapshotComposerCommandWithPastedImage(t *testing.T) {
	for _, key := range []string{"\t", "\r"} {
		t.Run(key, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			path := answerImageFixture(t)
			appServerTestKeys(t, u, "\n\x1b[200~"+path+"\x1b[201~\x1b[H\x1b[D/bt")
			if !u.picker.open || u.picker.target.query != "bt" {
				t.Fatalf("image blocked command completion: %q %+v", u.Text, u.picker)
			}
			if key == "\t" {
				rows, _ := u.mainFrame(60, 10, 0)
				assertNativeUISnapshot(t, "composer-command-image", rows)
			}
			appServerTestKeys(t, u, key)
			draft := u.draftSnapshot()
			if key == "\r" {
				btwTestRequest(t, wire, "thread/fork", "main")
				draft = u.btw.pending
			} else if wire.Len() != 0 || !strings.HasPrefix(draft.Text, "/btw ") {
				t.Fatalf("Tab submitted or lost command: %q %s", draft.Text, wire.String())
			}
			if len(draft.Images) != 1 || draft.Images[0].Path != path || draft.Text[draft.Images[0].Start:draft.Images[0].End] != "[Image 1]" {
				t.Fatalf("completion lost attachment: %+v", draft)
			}
			if key == "\t" {
				appServerTestKeys(t, u, "\x1a")
				if u.Text != "/bt\n[Image 1] " || len(u.Images) != 1 {
					t.Fatalf("undo lost original draft: %q", u.Text)
				}
			}
		})
	}
}
