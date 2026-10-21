package launch

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func TestComposerCommandCatalog(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.attached.cwd = "" // Local commands do not depend on workspace discovery.
	appServerTestKeys(t, u, "/")
	if !u.picker.Open || len(u.picker.Choices) != len(nativeCommands) || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("catalog = %+v, requests = %s", u.picker, wire.Bytes())
	}
	frame := ansi.Strip(strings.Join(u.renderPicker(80, 8), "\n"))
	if !strings.Contains(frame, "/model") || !strings.Contains(frame, u.picker.Choices[0].Description) {
		t.Fatalf("missing command labels/descriptions: %s", frame)
	}
	appServerTestKeys(t, u, "\x1b[B\t")
	if u.Text != "/reasoning " || u.picker.Open || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("Tab did not complete locally: draft=%q picker=%+v", u.Text, u.picker)
	}
}

func TestComposerCommandEnterAndDismiss(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/sk")
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Name != "/skills" {
		t.Fatalf("filtered choices = %+v", u.picker.Choices)
	}
	appServerTestKeys(t, u, "\r")
	if u.picker.Kind != "menu" || u.Text != "" || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("Enter did not open skills menu: %+v", u.picker)
	}
	u, _ = newAppServerTestUI()
	appServerTestKeys(t, u, "/")
	u.pickerKey("\x1b")
	u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	if u.picker.Open || u.Text != "/" {
		t.Fatal("Escape must dismiss without changing the draft")
	}
	appServerTestKeys(t, u, "mod")
	if !u.picker.Open || len(u.picker.Choices) != 1 {
		t.Fatal("editing after dismissal must reopen filtered catalog")
	}
}

func TestComposerCommandQuerySelectsBestMatch(t *testing.T) {
	for _, key := range []string{"\t", "\r"} {
		t.Run(key, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestKeys(t, u, "/res"+key)
			if key == "\t" {
				if u.Text != "/resume " || u.picker.Open || wire.Len() != 0 {
					t.Fatalf("Tab completion: draft=%q open=%v requests=%s", u.Text, u.picker.Open, wire.String())
				}
			} else if u.resumePicker == nil || u.Text != "" {
				t.Fatalf("Enter did not open resume: draft=%q picker=%+v", u.Text, u.picker)
			}
		})
	}
}

func TestComposerCommandQueryEditingAndNavigation(t *testing.T) {
	for _, tt := range []struct{ name, keys, want string }{
		{"backspace", "/res\x7f", "/reasoning "},
		{"edit after navigation", "/re\x1b[B" + "s", "/resume "},
		{"navigate after edit", "/res\x1b[B", "/reasoning "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, tt.keys)
			// Republishing the same query must not discard deliberate navigation.
			u.renderPicker(80, 8)
			appServerTestKeys(t, u, "\t")
			if u.Text != tt.want {
				t.Fatalf("completion after %q = %q, want %q", tt.keys, u.Text, tt.want)
			}
		})
	}
}

func TestUISnapshotComposerCommandBestMatch(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "/res")
	assertNativeUISnapshot(t, "composer-command-best-match", u.renderPicker(80, 8))
}

func TestComposerCommandBoundaries(t *testing.T) {
	for _, draft := range []string{"hello /", "/model value", "/skills\ntext", "\x1b[200~/skills\x1b[201~"} {
		t.Run(draft, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			appServerTestKeys(t, u, draft)
			if u.picker.Open {
				t.Fatalf("unexpected catalog for %q", draft)
			}
		})
	}
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/nonexistent\r")
	if !u.attached.feedback.Alert || !strings.Contains(u.attached.feedback.Text, "Unknown command") || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("unknown command leaked or lacked feedback: %q", u.attached.feedback.Text)
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
		settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
		if u.shell.Main.Menu.Top > u.shell.Main.Menu.Selected || u.shell.Main.Menu.Top+u.shell.Main.Menu.RowCount <= u.shell.Main.Menu.Selected {
			t.Fatal("selection outside visible viewport")
		}
	}
}

func TestComposerCommandMouseSelection(t *testing.T) {
	u, wire := newAppServerTestUI()
	shell := terminalTest(u)
	appServerTestKeys(t, u, "/sk")
	u.renderPicker(80, 4)
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	u.shell.Main.Menu.Rect = terminalsession.Rect{0, 0, 80, 4}
	if err := shell.mouse("\x1b[<0;2;2M"); err != nil {
		t.Fatal(err)
	}
	if u.picker.Kind != "menu" || len(pickerRequests(t, wire)) != 0 {
		t.Fatalf("click did not dispatch local command: %+v", u.picker)
	}
}

func TestUISnapshotComposerCommandWithPastedImage(t *testing.T) {
	for _, key := range []string{"\t", "\r"} {
		t.Run(key, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			path := answerImageFixture(t)
			appServerTestKeys(t, u, "\n\x1b[200~"+path+"\x1b[201~\x1b[H\x1b[D/bt")
			if !u.picker.Open || u.picker.Target.Query != "bt" {
				t.Fatalf("image blocked command completion: %q %+v", u.Text, u.picker)
			}
			if key == "\t" {
				rows, _ := u.testMainFrame(60, 10, 0)
				assertNativeUISnapshot(t, "composer-command-image", rows)
			}
			appServerTestKeys(t, u, key)
			draft := draftSnapshot(u.Editor)
			if key == "\r" {
				btwTestRequest(t, wire, "thread/fork", "main")
				draft = u.BTW.Pending
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
