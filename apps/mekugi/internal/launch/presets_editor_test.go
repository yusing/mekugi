package launch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/conversation/completion"
)

func TestPresetExternalEditorHandoff(t *testing.T) {
	for _, outcome := range []string{"save", "save-refused", "retry", "failure", "invalid", "replaced-menu", "changed-stream"} {
		t.Run(outcome, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			script := filepath.Join(dir, "editor")
			body := "#!/bin/sh\ncat \"$1\" > \"$1.seed\"\nprintf 'edited preset' > \"$1\"\n"
			if outcome == "failure" {
				body += "exit 1\n"
			} else if outcome == "invalid" {
				body += "printf '!shell' > \"$1\"\n"
			}
			if err := os.WriteFile(script, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EDITOR", script)
			u, _ := newAppServerTestUI()
			u.startSession("main", dir)
			c := nativeTestPromptClient(u)
			c.ctx = t.Context()
			c.editor("main").InsertText("ordinary draft")
			event := c.event
			event.Frame.View.Menu = &sessionpresentation.MenuView{ID: 7, Kind: "preset-edit", Query: "preset seed", Choices: []completion.Choice{{Name: "continue"}}}
			if outcome == "retry" {
				event.Frame.View.Menu.Problem = "Preset could not be saved"
			}
			c.accept(event)
			if outcome == "retry" {
				if c.handoff != nil {
					t.Fatal("storage error reopened the editor before showing its failure")
				}
				c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\r"})
			}
			handoff := c.handoff
			if handoff == nil || handoff.menu == nil {
				t.Fatal("entering the preset editor did not request terminal handoff")
			}
			// Navigation cannot redirect an editor result into an ordinary draft.
			c.event.Frame.View.Input = sessionpresentation.Thread{ID: "side"}
			c.event.Frame.View.Displayed = c.event.Frame.View.Input
			c.event.Frame.View.Version.Revision++
			if outcome == "replaced-menu" {
				c.event.Frame.View.Menu = &sessionpresentation.MenuView{ID: 8, Kind: "preset-edit"}
			} else if outcome == "changed-stream" {
				c.event.Attachment = "replacement"
			}
			c.refresh(time.Now())
			c.calls = nil
			c.completionReady = true
			_, c.completionTarget = c.selectedEditor()
			c.completionInput, c.completionCatalog = c.event.Frame.View.Input.ID, c.event.Frame.CompletionCatalog
			runtime := &nativeClientRuntime{ctx: t.Context(), active: c}
			if err := c.external(t.Context(), nil, nil, handoff, runtime); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(dir, "mekugi-draft-*.txt"))
			if err != nil || len(files) != 1 {
				t.Fatalf("recovery file: %v, %v", files, err)
			}
			seed, err := os.ReadFile(files[0] + ".seed")
			if err != nil || string(seed) != "preset seed" {
				t.Fatalf("external editor seed %q: %v", seed, err)
			}
			var saved *nativeClientCall
			for _, call := range c.calls {
				if call.operation.Menu != nil {
					saved = call
				}
			}
			if outcome == "save" || outcome == "save-refused" || outcome == "retry" {
				if saved == nil || saved.operation.Menu.Menu != 7 || saved.operation.Menu.Query != "edited preset" || saved.operation.Menu.Thread != "main" || saved.operation.Menu.Version != saved.target.Version {
					t.Fatal("editor did not return text to the original preset menu")
				}
				if saved.operation.Editor.Draft.Text != "ordinary draft" {
					t.Fatal("editor result carried another destination's draft")
				}
				if outcome == "save-refused" {
					saved.after(errors.New("menu closed"))
					if _, err := os.Stat(files[0]); err != nil || !strings.Contains(c.notice, files[0]) {
						t.Fatal("refused save lost the recovery file")
					}
				} else {
					saved.after(nil)
					if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
						t.Fatal("successful editor file was not cleaned up")
					}
				}
			} else if saved != nil || !strings.Contains(c.notice, files[0]) {
				t.Fatalf("failed editor should retain its recovery file without saving: %s", c.notice)
			}
			if c.editor("main").Text != "ordinary draft" || c.editor("side:side").Text != "" {
				t.Fatal("preset editing changed an ordinary draft")
			}
		})
	}
}
