package launch

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

func TestNativeDetachDuringWork(t *testing.T) {
	for _, command := range []string{"/detach", "/quit"} {
		t.Run(command, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			c := nativeTestPromptClient(u)
			c.event.Frame.View.InterruptAvailable = true
			c.editor("main").InsertText(command)
			c.calls = nil
			c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}})
			if !c.quit || c.editor("main").Text != "" || len(c.calls) != 0 {
				t.Fatal("local exit did not clear its draft without sending host work")
			}
		})
	}
}

func TestNativeDetachPresetWaitsForSubmission(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	c := nativeTestPromptClient(u)
	c.editor("main").InsertText("keep draft")
	c.calls = nil
	c.event.Frame.Effects = []sessionapi.NativeEffect{{ID: 1, Thread: "main", Recall: new("/detach")}}
	c.applyEffects()
	if c.quit || c.editor("main").Text != "/detach" {
		t.Fatal("preset detach did not wait for explicit submission")
	}
	for _, call := range c.calls {
		if call.operation.Receipt == nil {
			t.Fatal("preset detach submitted host work")
		}
	}
	c.calls = nil
	c.composer(terminalsession.ComposerInput{Command: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}})
	if !c.quit || c.editor("main").Text != "" || len(c.calls) != 0 {
		t.Fatal("explicitly submitted preset did not detach locally")
	}
}
