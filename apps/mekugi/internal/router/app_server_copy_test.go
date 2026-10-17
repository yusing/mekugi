package router

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
)

func TestResponseCopyChoices(t *testing.T) {
	for _, tc := range []struct {
		name, source   string
		labels, values []string
	}{
		{"plain", "  hello **世界**  \n", nil, nil},
		{"fences", "```go,example\n\tf()  \n```\n\n~~~sh\nprintf hi\n~~~\n", []string{"go code", "sh code"}, []string{"\tf()  \n", "printf hi\n"}},
		{"crlf", "```text\r\na  \r\nb\r\n```\r\n", []string{"text code"}, []string{"a  \r\nb\r\n"}},
		{"tab info", "```go\texample\nf()\n```\n", []string{"go code"}, []string{"f()\n"}},
		{"quote", "> **hello**\n>\n> > nested\n\nafter", []string{"Blockquote"}, []string{"**hello**\n\n> nested\n"}},
		{"list quote", "- > hello\n", []string{"Blockquote"}, []string{"hello\n"}},
		{"ordered list quote", "1. > hello\n   > world\n\n2. next\n", []string{"Blockquote"}, []string{"hello\nworld\n"}},
		{"adjacent list quotes", "- > one\n- > two\n", []string{"Blockquote", "Blockquote"}, []string{"one\n", "two\n"}},
		{"quote eof", "> hello", []string{"Blockquote"}, []string{"hello"}},

		{"lazy quote", "> first\ncontinuation\n\nlast", []string{"Blockquote"}, []string{"first\ncontinuation\n"}},
		{"quoted code", "> ```sh\n> echo hi\n> ```\n> prose\n", []string{"Blockquote", "sh code"}, []string{"```sh\necho hi\n```\nprose\n", "echo hi\n"}},
		{"only quoted code", "> ```\n> hi\n> ```\n", []string{"Code block"}, []string{"hi\n"}},
		{"not quote", "```\n> code\n```\n\n    > indented\n", []string{"Code block"}, []string{"> code\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := responseCopyChoices(tc.source)
			labels, values := []string{}, []string{}
			for _, choice := range got {
				labels = append(labels, choice.Name)
				values = append(values, choice.copyText)
			}
			wantLabels := append([]string{"Whole response"}, tc.labels...)
			wantValues := append([]string{tc.source}, tc.values...)
			if !reflect.DeepEqual(labels, wantLabels) || !reflect.DeepEqual(values, wantValues) {
				t.Fatalf("labels=%q values=%q; want %q %q", labels, values, wantLabels, wantValues)
			}
		})
	}
}

func TestAppServerCopyPicker(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	applyTranscriptFixture(u.testActivity(u.view), true, "", "main", "main", "old", "answer", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "Answer.\n\n```sh\nprintf '界'  \n```\n"})
	applyTranscriptFixture(u.testActivity(u.view), true, "", "main", "child", "t", "child", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "Not this"})
	applyTranscriptFixture(u.testActivity(u.view), true, "", "main", "main", "new", "stream", "item/agentMessage/delta", "Partial", binding.ThreadItem{})
	u.Turn = "new"
	appServerTestKeys(t, u, "/cop\r")
	if u.picker.modal != "copy" || u.Text != "" || len(u.picker.Choices) != 2 {
		t.Fatalf("picker=%+v draft=%q", u.picker, u.Text)
	}
	frame := ansi.Strip(strings.Join(u.renderPicker(80, 8), "\n"))
	if !strings.Contains(frame, "Copy to clipboard") || !strings.Contains(frame, "Whole response") {
		t.Fatalf("frame=%s", frame)
	}
	appServerTestKeys(t, u, "\x1b[200~pasted\rtext\x1b[201~")
	if u.Text != "" || u.shell.Frame.Clipboard != "" || u.picker.modal != "copy" {
		t.Fatal("paste changed draft or copied a choice")
	}
	appServerTestKeys(t, u, "\x1b[B\r")
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("printf '界'  \n")) + "\x07"
	if u.shell.Frame.Clipboard != want || u.picker.Open || u.Turn != "new" || wire.Len() != 0 {
		t.Fatalf("clipboard=%q picker=%+v wire=%s", u.shell.Frame.Clipboard, u.picker, wire.Bytes())
	}
	if u.feedback.Alert || u.feedback.Text != "Copy sent to terminal clipboard" {
		t.Fatalf("feedback=%q", u.feedback.Text)
	}
	screen := vt.NewEmulator(100, 30)
	defer screen.Close()
	if err := u.testPaint(screen, 100, 30); err != nil {
		t.Fatal(err)
	}
	if u.shell.Frame.Clipboard != "" {
		t.Fatal("clipboard request not consumed by paint")
	}
}

func TestAppServerCopyEmptyCancelAndRestore(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	appServerTestKeys(t, u, "/copy\r")
	if u.feedback.Text != "No agent response to copy" || u.shell.Frame.Clipboard != "" || wire.Len() != 0 {
		t.Fatalf("empty notice=%q wire=%s", u.feedback.Text, wire.Bytes())
	}
	u.startSession("main", "")
	u.restoreMainHistory([]binding.HistoryTurn{{ID: "old", Status: "completed", Items: []binding.ThreadItem{{ID: "a", Type: "agentMessage", Text: "restored"}}}}, nil, nil)
	appServerTestKeys(t, u, "/copy\r")
	if u.picker.copyTexts[0] != "restored" {
		t.Fatal("resume lost copy source")
	}
	u.pickerKey("\x1b")
	u.refreshPicker()
	if u.picker.Open || u.shell.Frame.Clipboard != "" || u.Text != "" {
		t.Fatal("cancel copied or reopened")
	}
	u.Thread = "different"
	appServerTestKeys(t, u, "/copy\r")
	if u.feedback.Text != "No agent response to copy" {
		t.Fatal("borrowed another thread's response")
	}
}

func TestTerminalClipboardSharedSelection(t *testing.T) {
	for _, key := range []byte{'c', 3} {
		u, wire := newAppServerTestUI()
		u.ensureShell()
		u.Turn = "active"
		u.shell.Selection = &terminalsession.SourceSelection{Selection: selectedTerminalText("same text")}
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
		selected := u.shell.Frame.Clipboard
		u.shell.Frame.Clipboard = ""
		applyTranscriptFixture(u.testActivity(u.view), true, "", "main", "main", "t", "a", "item/completed", "", binding.ThreadItem{Type: "agentMessage", Text: "same text"})
		appServerTestKeys(t, u, "/copy\r\r")
		if selected == "" || u.shell.Frame.Clipboard != selected || u.Turn != "active" || wire.Len() != 0 {
			t.Fatalf("key=%d selected=%q copy=%q wire=%s", key, selected, u.shell.Frame.Clipboard, wire.Bytes())
		}
	}
}
