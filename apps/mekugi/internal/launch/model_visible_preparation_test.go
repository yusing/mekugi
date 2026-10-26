package launch

import (
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestModelVisiblePreparedRequestThroughNativeDialogs(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	proxy.inputProjections = testInputProjections()
	trace := newNativeTraceFixture(t)
	proxy.nativeTrace = nativetrace.New(trace.root)
	u, writer := newAppServerTestUI()
	u.audience = proxy.activity
	u.attached.cwd = t.TempDir()
	// The real thread/start response installs both the audience and production
	// detail lookup, before host history or any prepared content exists.
	replaceAppServerTestSession(t, u, "thread")
	writer.Reset()
	var body strings.Builder
	for i := range 40 {
		fmt.Fprintf(&body, "distinctive source row %02d with attachment content\n", i)
	}
	file, skill := body.String(), "# Attached guide\n"+body.String()
	var input []any
	var history []binding.ThreadItem
	var second attachments.Snapshot
	for i := range 2 {
		frames := append(composer.FileFrames("/workspace/file.txt", file), composer.SkillFrames("guide", "", skill)...)
		draft := conversation.Input{Text: "inspect snapshots", Attachments: []string{attachments.Encode(frames)}}
		if err := sendConversationInput(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, []conversation.Input{draft}, false); err != nil {
			t.Fatal(err)
		}
		sent := appServerTurnRequests(t, writer)
		if len(sent) != 1 {
			t.Fatalf("submissions: %d", len(sent))
		}
		var parts []any
		for _, part := range sent[0].Params.Input {
			parts = append(parts, map[string]string{"type": "input_text", "text": part.Text})
		}
		input = append(input, map[string]any{"role": "user", "content": parts})
		content, _ := json.Marshal(sent[0].Params.Input)
		history = append(history, binding.ThreadItem{Type: "userMessage", ID: fmt.Sprint("user-", i), Content: content})
		if i == 1 {
			var ok bool
			second, ok = attachments.DecodeSnapshot(sent[0].Params.Input[1].Text)
			if !ok {
				t.Fatal("missing submitted identity")
			}
		}
	}
	const patch = "*** Begin Patch\n*** Add File: one.txt\n+one\n*** Add File: two.txt\n+two\n*** End Patch"
	source := `text(await tools.exec_command({cmd:"echo original"})); text(await tools.apply_patch(` + string(responses.MustMarshalJSON(patch)) + `));`
	trace.start("thread", "runtime", "cell", source)
	trace.tool("thread", "runtime", "command", "exec_command", `{"cmd":"echo original"}`)
	trace.result("thread", "command", "completed", map[string]any{"exit_code": 0})
	trace.tool("thread", "runtime", "patch", "apply_patch", patch)
	trace.result("thread", "patch", "completed", "Success")
	trace.end("thread", "runtime")
	input = append(input, duplicateTestInput("first", "earlier-cell", file)...)
	input = append(input, continuationTestCall("exec", "cell", source), map[string]any{"type": "custom_tool_call_output", "call_id": "cell", "output": []any{map[string]string{"type": "input_text", "text": duplicateTestHeader + file}}})
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "input": input, "tools": testExecResponsesTools()}))
	if err != nil {
		t.Fatal(err)
	}
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"id":"response","status":"completed","output":[]}`)}}}
	headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{u.attached.cwd: nil})
	headers.Set(codexresponses.ThreadIDHeader, "thread")
	if err := executeRequest(t.Context(), t.Context(), request, headers, "session", provider, io.Discard, testCriticalErrors(), proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatal("request was not forwarded once")
	}
	forwarded, err := codexresponses.ParseRequest(provider.forwarded[0])
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(forwarded.Fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	var preparedFile, preparedSkill, preparedCell string
	for _, item := range items {
		if responses.StringField(item, "call_id") == "cell" && responses.StringField(item, "type") == "custom_tool_call_output" {
			var parts []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(item["output"], &parts); err != nil || len(parts) != 1 {
				t.Fatal("missing output text part")
			}
			preparedCell = parts[0].Text
		}
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(item["content"], &parts)
		for _, part := range parts {
			if strings.HasPrefix(part.Text, "Attached file ") {
				preparedFile = part.Text
			}
			if strings.HasPrefix(part.Text, "Attached skill ") {
				preparedSkill = part.Text
			}
		}
	}
	if !strings.Contains(preparedFile, "\nSee O") || !strings.Contains(preparedSkill, "\nSee O") || preparedCell == "" {
		t.Fatal("provider did not receive transformed file, skill, and cell output")
	}
	history = append(history, binding.ThreadItem{ID: "command", Type: "commandExecution", Command: "echo original", AggregatedOutput: new("original command output\n"), ExitCode: new(0), Status: "completed"})
	u.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: history}})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": "patch", "type": "fileChange", "status": "completed", "changes": []any{
		map[string]any{"path": "one.txt", "kind": map[string]any{"type": "add"}, "diff": "+one\n"},
		map[string]any{"path": "two.txt", "kind": map[string]any{"type": "add"}, "diff": "+two\n"},
	}}})
	terminal := terminalTest(u)
	checked := 0
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Native == nil {
			continue
		}
		if entry.Source.Native.Item == "patch" {
			if !terminal.openActivityEdit(u.shell.Transcript, entry.Source.Seq, "two.txt") {
				t.Fatal("patch details unavailable")
			}
			terminal.layoutOutput(80)
			if !strings.Contains(terminal.Dialog.Document.Text, "+two") {
				t.Fatal("selected patch page shifted")
			}
			original := terminal.Dialog.Document.Text
			terminal.outputKey("o")
			terminal.layoutOutput(80)
			if terminal.Dialog.Document.Text != preparedCell || !strings.Contains(terminal.Dialog.Document.Detail, "Whole execution cell cell") {
				t.Fatal("patch lost actual whole-cell content")
			}
			terminal.outputKey("o")
			terminal.layoutOutput(80)
			if terminal.Dialog.Document.Text != original {
				t.Fatal("patch source changed")
			}
			checked++
			continue
		}
		if entry.Source.Native.Item != "command" && entry.Source.Native.Item != "user-1/attachments" {
			continue
		}
		if !terminal.openEntry(u.shell.Transcript, entry.Source.Seq) {
			continue
		}
		for i, page := range terminal.Dialog.Pages {
			want, original := "", ""
			switch page.AttachmentID {
			case second.ReceiptID(second.Frames[0]):
				want, original = preparedFile, file
			case second.ReceiptID(second.Frames[len(second.Frames)-1]):
				want, original = preparedSkill, skill
			default:
				if entry.Source.Native.Item == "command" {
					want, original = preparedCell, "original command output"
				}
			}
			if want == "" {
				continue
			}
			terminal.Dialog.ShowPage(i)
			terminal.layoutOutput(80)
			if terminal.Dialog.Document.Text != original {
				t.Fatalf("original copy changed for %s: got length %d tail %q; want length %d tail %q", page.Verb, len(terminal.Dialog.Document.Text), terminal.Dialog.Document.Text[max(0, len(terminal.Dialog.Document.Text)-20):], len(original), original[max(0, len(original)-20):])
			}
			terminal.outputKey("o")
			terminal.layoutOutput(80)
			if terminal.Dialog.Document.Text != want {
				t.Fatalf("tab differs from actual prepared request: %q", terminal.Dialog.Document.Text)
			}
			terminal.outputKey("o")
			checked++
		}
	}
	if checked != 4 {
		t.Fatalf("checked %d details, want file, skill, command, patch", checked)
	}
}
