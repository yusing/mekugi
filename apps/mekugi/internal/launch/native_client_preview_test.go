package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/terminalui"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
)

func TestNativeClientQuestionPreviewSurvivesRefresh(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("release step %02d", i))
	}
	questionTestMessage(t, u, nil, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: "preview", Type: "agentMessage", Delivery: "async", Questions: []binding.Question{{ID: "q1", Question: "Release plan?", Options: []binding.QuestionOption{{Label: "Immediate"}, {Label: "Staged", Recommended: true, Preview: strings.Join(lines, "\n")}}}}}})
	host := nativePendingQuestions(u.attached.conversation)
	if host[0].Questions[0].Selected != 1 || !host[0].Questions[0].Choices[1].Recommended {
		t.Fatal("host lost recommendation")
	}
	client := nativeTestPromptClient(u)
	if client.prompts.questions.Current().Selected != 1 || client.prompts.questions.Current().Done || wire.Len() != 0 {
		t.Fatal("recommendation not focused or auto-submitted")
	}
	dock := client.prompts.questions.View("main")
	layout := client.renderer.terminal.Prompts.QuestionLayout(dock)
	paint := func() string {
		return ansi.Strip(strings.Join(conversationui.Questions(client.prompts.questions.View("main"), &terminalui.Painter{}, layout, "", false, 96, 12), "\n"))
	}
	if !strings.Contains(paint(), "release step 01") {
		t.Fatal("initial preview absent")
	}
	layout.Key(dock, "\x1b[1;4B", true)
	layout.Key(dock, "\x1b[1;4B", true)
	before := paint()
	frame := client.event.Frame
	frame.Questions = nativePendingQuestions(u.attached.conversation)
	client.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
	if client.renderer.terminal.Prompts.QuestionLayout(client.prompts.questions.View("main")) != layout || paint() != before || strings.Contains(before, "release step 01") {
		t.Fatal("refresh reset preview scroll")
	}
	nativeTestPromptKey(t, client, "\t")
	if !client.prompts.questions.Current().Note || len(client.calls) != 0 || wire.Len() != 0 {
		t.Fatal("async note auto-submitted")
	}
	key := layout.Key(client.prompts.questions.View("main"), "\x1b[A", true)
	if !client.prompts.questions.ApplyInput(key.Question) {
		t.Fatal("focus edit refused")
	}
	if strings.Contains(paint(), "release step") {
		t.Fatal("plain option kept stale preview")
	}
	frame.Questions = nativePendingQuestions(u.attached.conversation)
	client.accept(sessionapi.NativeEvent{Attachment: "client", Frame: frame})
	if client.prompts.questions.Current().Selected != 0 || strings.Contains(paint(), "release step") {
		t.Fatal("refresh overwrote local focus")
	}
}
