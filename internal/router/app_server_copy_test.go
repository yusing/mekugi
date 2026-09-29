package router

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
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
				labels = append(labels, choice.name)
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
	u.view.applyAppServerItem("", "main", "main", "old", "answer", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Answer.\n\n```sh\nprintf '界'  \n```\n"})
	u.view.applyAppServerItem("", "main", "child", "t", "child", "item/completed", "", appServerItem{Type: "agentMessage", Text: "Not this"})
	u.view.applyAppServerItem("", "main", "main", "new", "stream", "item/agentMessage/delta", "Partial", appServerItem{})
	u.turn = "new"
	appServerTestKeys(t, u, "/cop\r")
	if u.picker.modal != "copy" || u.draft != "" || len(u.picker.choices) != 2 {
		t.Fatalf("picker=%+v draft=%q", u.picker, u.draft)
	}
	frame := ansi.Strip(strings.Join(u.renderPicker(80, 8), "\n"))
	if !strings.Contains(frame, "Copy to clipboard") || !strings.Contains(frame, "Whole response") {
		t.Fatalf("frame=%s", frame)
	}
	appServerTestKeys(t, u, "\x1b[200~pasted\rtext\x1b[201~")
	if u.draft != "" || u.shell.clipboard != "" || u.picker.modal != "copy" {
		t.Fatal("paste changed draft or copied a choice")
	}
	appServerTestKeys(t, u, "\x1b[B\r")
	want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("printf '界'  \n")) + "\x07"
	if u.shell.clipboard != want || u.picker.open || u.turn != "new" || wire.Len() != 0 {
		t.Fatalf("clipboard=%q picker=%+v wire=%s", u.shell.clipboard, u.picker, wire.Bytes())
	}
	if u.noticeAlert || u.notice != "Copy sent to terminal clipboard" {
		t.Fatalf("feedback=%q", u.notice)
	}
	screen := vt.NewEmulator(100, 30)
	defer screen.Close()
	if err := u.paint(screen, 100, 30); err != nil {
		t.Fatal(err)
	}
	if u.shell.clipboard != "" {
		t.Fatal("clipboard request not consumed by paint")
	}
}

func TestAppServerCopyEmptyCancelAndRestore(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ensureShell()
	appServerTestKeys(t, u, "/copy\r")
	if u.notice != "No agent response to copy" || u.shell.clipboard != "" || wire.Len() != 0 {
		t.Fatalf("empty notice=%q wire=%s", u.notice, wire.Bytes())
	}
	u.session.start("main", "")
	u.restoreMainHistory([]appServerHistoryTurn{{ID: "old", Status: "completed", Items: []appServerItem{{ID: "a", Type: "agentMessage", Text: "restored"}}}}, nil, nil)
	appServerTestKeys(t, u, "/copy\r")
	if u.picker.choices[0].copyText != "restored" {
		t.Fatal("resume lost copy source")
	}
	u.pickerKey("\x1b")
	u.refreshPicker()
	if u.picker.open || u.shell.clipboard != "" || u.draft != "" {
		t.Fatal("cancel copied or reopened")
	}
	u.thread = "different"
	appServerTestKeys(t, u, "/copy\r")
	if u.notice != "No agent response to copy" {
		t.Fatal("borrowed another thread's response")
	}
}

func TestAppServerCopyJournal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "a", Text: "First"}, terminal: true, batch: 1})
	u.view.applyJournal("main", nativeJournalPublication{item: journalItem{ID: "b", Text: "Second"}, terminal: true, batch: 1})
	u.showCopyPicker()
	if u.picker.choices[0].copyText != "First\n\nSecond" {
		t.Fatalf("journal copy=%q", u.picker.choices[0].copyText)
	}
}

func TestTerminalClipboardSharedSelection(t *testing.T) {
	for _, key := range []byte{'c', 3} {
		u, wire := newAppServerTestUI()
		u.ensureShell()
		u.turn = "active"
		u.shell.selection = &terminalSelection{rect: terminalRect{w: 9, h: 1}, rows: []string{"same text"}, startX: 0, startY: 0, endX: 9, endY: 0}
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
		selected := u.shell.clipboard
		u.shell.clipboard = ""
		u.view.applyAppServerItem("", "main", "main", "t", "a", "item/completed", "", appServerItem{Type: "agentMessage", Text: "same text"})
		appServerTestKeys(t, u, "/copy\r\r")
		if selected == "" || u.shell.clipboard != selected || u.turn != "active" || wire.Len() != 0 {
			t.Fatalf("key=%d selected=%q copy=%q wire=%s", key, selected, u.shell.clipboard, wire.Bytes())
		}
	}
}
