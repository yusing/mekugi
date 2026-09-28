package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestComposerCommandCatalog(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "" // Local commands do not depend on workspace discovery.
	appServerTestKeys(t, u, "/")
	if !u.picker.open || len(u.picker.choices) != 6 || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("catalog = %+v, requests = %s", u.picker, wire.Bytes())
	}
	frame := ansi.Strip(strings.Join(u.renderPicker(80, 8), "\n"))
	if !strings.Contains(frame, "/model") || !strings.Contains(frame, "Choose the model") {
		t.Fatalf("missing command labels/descriptions: %s", frame)
	}
	appServerTestKeys(t, u, "\x1b[B\t")
	if u.draft != "/reasoning " || u.picker.open || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("Tab did not complete locally: draft=%q picker=%+v", u.draft, u.picker)
	}
}

func TestComposerCommandEnterAndDismiss(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/sk")
	if len(u.picker.choices) != 1 || u.picker.choices[0].name != "/skills" {
		t.Fatalf("filtered choices = %+v", u.picker.choices)
	}
	appServerTestKeys(t, u, "\r")
	if u.picker.modal != "menu" || u.draft != "" || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("Enter did not open skills menu: %+v", u.picker)
	}
	u, _ = newAppServerTestUI()
	appServerTestKeys(t, u, "/")
	u.pickerKey("\x1b")
	u.refreshPicker()
	if u.picker.open || u.draft != "/" {
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
	if !u.noticeAlert || !strings.Contains(u.notice, "Unknown command") || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("unknown command leaked or lacked feedback: %q", u.notice)
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
	u.picker.rect = terminalRect{0, 0, 80, 4}
	if err := shell.mouse("\x1b[<0;2;2M"); err != nil {
		t.Fatal(err)
	}
	if u.picker.modal != "menu" || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("click did not dispatch local command: %+v", u.picker)
	}
}
