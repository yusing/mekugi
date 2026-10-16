package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestNativePromptCoveredFrameDoesNotAdmitAnswer(t *testing.T) {
	for _, kind := range []string{"question", "approval"} {
		t.Run(kind, func(t *testing.T) {
			u, input := newAppServerTestUI()
			u.Turn = "turn"
			u.session.start("main", "")
			u.ensureShell()
			content := []activityui.Block{{Kind: "op", Verb: "Read", Code: "retained output", Body: strings.Repeat("Content dialog\n", 30)}}
			u.shell.openBlocks(u.view, content)
			send := func(keys string) {
				t.Helper()
				for _, key := range []byte(keys) {
					if err := u.shell.key(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			title, choice := "Which release scope?", "Narrow"
			if kind == "question" {
				questionTestSync(t, u, "prompt-request", false)
			} else {
				approvalTestCommand(t, u, "prompt-request", nil)
				title, choice = "Run this command?", "Yes, proceed"
			}
			screen := vt.NewEmulator(80, 20)
			defer screen.Close()
			if err := u.paint(screen, 80, 20); err != nil {
				t.Fatal(err)
			}
			if frame := screen.String(); !strings.Contains(frame, "Content dialog") || strings.Contains(frame, title) || strings.Contains(frame, choice) {
				t.Fatalf("dialog did not obscure the prompt:\n%s", frame)
			}
			// A close and answer can be drained before the next frame tick.
			send("q\r")
			if u.shell.output != nil {
				t.Fatal("close key did not dismiss the dialog")
			}
			if input.Len() != 0 {
				t.Fatalf("unseen prompt accepted an answer: %s", input.String())
			}
			// Explicit reopening must still reveal and answer the same host request.
			send("\x02q")
			if err := u.paint(screen, 80, 20); err != nil {
				t.Fatal(err)
			}
			if frame := screen.String(); !strings.Contains(frame, title) || !strings.Contains(frame, choice) {
				t.Fatalf("reopened prompt is not visible:\n%s", frame)
			}
			// A later overlay does not revoke readiness from an already seen prompt.
			u.shell.openBlocks(u.view, content)
			if err := u.paint(screen, 80, 20); err != nil {
				t.Fatal(err)
			}
			send("q\r")
			if response := input.String(); !strings.Contains(response, `"id":"prompt-request"`) || !strings.Contains(response, `"result":`) {
				t.Fatalf("visible prompt did not answer its original host request: %s", response)
			}
		})
	}
}

func TestNativePromptSelectionVisibility(t *testing.T) {
	for _, kind := range []string{"question", "approval"} {
		for _, covering := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/covering=%t", kind, covering), func(t *testing.T) {
				u, input := newAppServerTestUI()
				u.session.start("main", "")
				u.Turn = "turn"
				screen := vt.NewEmulator(80, 20)
				defer screen.Close()
				if err := u.paint(screen, 80, 20); err != nil {
					t.Fatal(err)
				}
				// The arriving dock has not replaced the terminal's previous frame yet.
				if kind == "question" {
					questionTestSync(t, u, "selected-prompt", false)
				} else {
					approvalTestCommand(t, u, "selected-prompt", nil)
				}
				x, y := u.composerRect.X+2, u.composerRect.Y
				if covering {
					x, y = u.view.FeedLeft+1, u.view.FeedTop
				}
				x, y = x+u.shell.layout.Main.X+1, y+u.shell.layout.Main.Y+1
				for _, key := range []byte(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<32;%d;%dM\x1b[<0;%d;%dm", x, y, x+3, y, x+3, y)) {
					if err := u.shell.key(key); err != nil {
						t.Fatal(err)
					}
				}
				if u.shell.selection == nil || !u.shell.selection.Moved() {
					t.Fatal("pointer drag did not freeze the preceding frame")
				}
				if err := u.paint(screen, 80, 20); err != nil {
					t.Fatal(err)
				}
				title := "Which release scope?"
				if kind == "approval" {
					title = "Run this command?"
				}
				if visible := strings.Contains(screen.String(), title); visible == covering {
					t.Fatalf("prompt visibility=%t with covering=%t:\n%s", visible, covering, screen.String())
				}
				if err := u.shell.key('\r'); err != nil {
					t.Fatal(err)
				}
				if answered := strings.Contains(input.String(), `"id":"selected-prompt"`); answered == covering {
					t.Fatalf("prompt answered=%t with covering=%t: %s", answered, covering, input.String())
				}
			})
		}
	}
}
