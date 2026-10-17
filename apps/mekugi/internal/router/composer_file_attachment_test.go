package router

import (
	"bytes"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposerFileAttachmentSnapshotAndProjection(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "file name.go")
	if err := os.WriteFile(path, []byte("package example\n// original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	u, w := newAppServerTestUI()
	u.session.cwd = cwd
	u.Text = "Review "
	bindComposerFile(u, `@"file name.go"`, "file name.go")
	u.Text += " please"
	draft := u.takeDraft()
	if err := os.WriteFile(path, []byte("changed after queueing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := u.send([]conversationmodel.Input{draft}, false); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || len(requests[0].Params.Input) != 2 {
		t.Fatalf("expected prompt and attachment input: %+v", requests)
	}
	input := requests[0].Params.Input
	if input[0].Text != draft.Text {
		t.Fatalf("changed inline placeholder: %q", input[0].Text)
	}
	frames, ok := attachments.Decode(input[1].Text)
	if !ok || len(frames) != 1 || !strings.Contains(frames[0], attachments.ReadGuidance) || !strings.HasSuffix(frames[0], "package example\n// original\n") {
		t.Fatalf("snapshot lost: %q", frames)
	}
	content, _ := json.Marshal(input)
	if text, _, ok := sessionpresentation.UserText(content); !ok || text != draft.Text {
		t.Fatalf("transport envelope leaked into transcript: %q", text)
	}
	resumed, _ := newAppServerTestUI()
	resumed.agents = activityui.NewView()
	resumed.session.start(resumed.Thread, cwd)
	resumed.restoreHistory([]binding.HistoryTurn{{ID: "saved", Status: "completed", Items: []binding.ThreadItem{{Type: "userMessage", ID: "saved-input", Content: content}}}})
	if len(resumed.InputHistory) != 1 || resumed.InputHistory[0].Text != draft.Text {
		t.Fatal("resume leaked attachment framing into recalled prompt")
	}
	providerParts := []map[string]any{{"type": "input_text", "text": input[0].Text}, {"type": "input_image", "image_url": "data:image/png;base64,test"}, {"type": "input_text", "text": input[1].Text}}
	raw := responses.MustMarshalJSON([]any{map[string]any{"type": "message", "role": "user", "content": providerParts}})
	if question := binding.SubmittedUserText(raw, "/root", false); question != draft.Text {
		t.Fatalf("attachment became naming text: %q", question)
	}
	request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": raw}}
	requestexecution.ProjectFileAttachments(&request)
	var messages []struct {
		Role    string              `json:"role"`
		Content []map[string]string `json:"content"`
	}
	if err := json.Unmarshal(request.Fields["input"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].Content) != 2 || messages[0].Content[0]["text"] != draft.Text || messages[0].Content[1]["type"] != "input_image" || messages[1].Role != "user" || messages[1].Content[0]["text"] != frames[0] {
		t.Fatalf("projection did not preserve prompt/media and separate file: %+v", messages)
	}
	first := bytes.Clone(request.Fields["input"])
	requestexecution.ProjectFileAttachments(&request)
	if !bytes.Equal(first, request.Fields["input"]) {
		t.Fatal("projection is not idempotent")
	}
	// A fresh request built only from persisted Codex input needs no live UI or file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replayed := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": raw}}
	requestexecution.ProjectFileAttachments(&replayed)
	if !responses.SameJSONValue(first, replayed.Fields["input"]) {
		t.Fatal("replay changed submitted snapshot")
	}
}

func TestComposerFileAttachmentWireAndProviderPrefix(t *testing.T) {
	request := modelTestRequest(t, "gpt-6-sol")
	raw := responses.MustMarshalJSON([]any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": "Review @example"},
		map[string]any{"type": "input_text", "text": attachments.Encode([]string{"Attached file /work/example:\nexact content"})},
	}}})
	request.Fields["input"] = raw
	requestexecution.ProjectFileAttachments(&request)
	body, err := request.WireBody(request.Fields)
	if err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Input []jsonv1.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &sent); err != nil || len(sent.Input) != 2 {
		t.Fatalf("wire did not carry separate user messages: %v", err)
	}
	for _, automatic := range []bool{false, true} {
		request.Fields["input"] = raw
		request.Fields["previous_response_id"] = responses.MustMarshalJSON("parent")
		requestexecution.ProjectFileAttachments(&request)
		exchange := confirmedHistoryExchange(t, sent.Input, automatic)
		if _, err := exchange.Reconcile(&request, true); err != nil {
			t.Fatal(err)
		}
		if request.CachedInput != 2 || request.RebaseInput {
			t.Fatal("replayed snapshot changed provider prefix")
		}
	}
}

func TestComposerFileAttachmentNoticeBudget(t *testing.T) {
	d := conversationmodel.Input{}
	for i := range 1200 {
		d.Files = append(d.Files, composer.Token{Path: strings.Repeat("missing", 25) + string(rune(0x4e00+i))})
	}
	d.SnapshotFiles(t.TempDir())
	if len(d.Attachments) != 1 || len(d.Attachments[0]) > attachments.Budget {
		t.Fatal("omission notices exceeded attachment budget")
	}
	frames, ok := attachments.Decode(d.Attachments[0])
	if !ok || !strings.Contains(frames[len(frames)-1], "remaining contents NOT ATTACHED") {
		t.Fatal("omitted references were not reported")
	}
}

func TestComposerFileAttachmentChunksAndBounds(t *testing.T) {
	for _, content := range []string{"", strings.Repeat("界", attachments.Chunk), strings.Repeat("row\r\n", 13000), strings.Repeat("\"\\\t", 10000)} {
		frames := composer.FileFrames("/work/example", content)
		var recovered strings.Builder
		for _, frame := range frames {
			_, body, ok := strings.Cut(frame, ":\n")
			if !ok || len(body) > attachments.Chunk {
				t.Fatalf("invalid frame size: %d", len(body))
			}
			recovered.WriteString(body)
		}
		if recovered.String() != content {
			t.Fatal("chunking changed file bytes")
		}
	}
	cwd := t.TempDir()
	for name, data := range map[string][]byte{
		"empty": {}, "binary": {0, 1}, "invalid": {0xff},
		"large":   bytes.Repeat([]byte("x"), attachments.Budget),
		"escaped": bytes.Repeat([]byte("\x01"), attachments.Budget/3),
	} {
		if err := os.WriteFile(filepath.Join(cwd, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := conversationmodel.Input{}
	for _, path := range []string{"empty", "empty", "binary", "invalid", "large", "escaped", "missing"} {
		d.Files = append(d.Files, composer.Token{Path: path})
	}
	d.SnapshotFiles(cwd)
	if len(d.Attachments) != 1 || len(d.Attachments[0]) > attachments.Budget || d.AttachmentNotice == "" {
		t.Fatal("attachment budget/notice missing")
	}
	frames, ok := attachments.Decode(d.Attachments[0])
	if !ok || len(frames) != 6 {
		t.Fatalf("deduplication or notices lost: %q", frames)
	}
	if !strings.HasSuffix(frames[0], ":\n") {
		t.Fatalf("empty file not attached: %q", frames[0])
	}
	for _, frame := range frames[1:] {
		if !strings.Contains(frame, "CONTENT NOT ATTACHED") {
			t.Fatalf("unreadable/oversize file silently lost: %q", frame)
		}
	}
	if _, err := composer.ReadFile("relative.txt"); err == nil {
		t.Fatal("relative file used process cwd")
	}
}

func TestComposerFileAttachmentProjectionLeavesLookalikes(t *testing.T) {
	for _, item := range []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "input_text", "text": attachments.Encode([]string{"Attached file data"})}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "quoted " + attachments.Encode([]string{"Attached file data"})}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": attachments.Prefix + "invalid" + attachments.Suffix}}},
	} {
		raw := responses.MustMarshalJSON([]any{item})
		request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": raw}}
		requestexecution.ProjectFileAttachments(&request)
		if !bytes.Equal(raw, request.Fields["input"]) {
			t.Fatal("changed non-attachment input")
		}
	}
}

func TestSkillAttachmentProjectionMatchesSuccessfulSource(t *testing.T) {
	const name = "review"
	selected := "<skill>\n<name>review</name>\n<path>/native/SKILL.md</path>\nnative instructions\n</skill>"
	for _, test := range []struct {
		name, path                   string
		omitted, compact, suppressed bool
	}{
		{name: "same source", path: "/native/SKILL.md", suppressed: true},
		{name: "different source", path: "/other/SKILL.md"},
		{name: "native omission", path: "/native/SKILL.md", omitted: true},
		{name: "managed precedence", suppressed: true},
		{name: "managed omission", omitted: true},
		{name: "managed compact reference", compact: true, suppressed: true},
		{name: "compact reference lacks native source", path: "/native/SKILL.md", compact: true},
	} {
		for _, multipart := range []bool{false, true} {
			t.Run(test.name+map[bool]string{false: "/string", true: "/parts"}[multipart], func(t *testing.T) {
				frames := composer.SkillFrames(name, test.path, "snapshot instructions")
				if test.omitted {
					frames = []string{strings.Split(frames[0], " (UTF-8 bytes ")[0] + `: CONTENT NOT ATTACHED ("too large"). Read this separately if needed.`}
				}
				text := selected
				if test.compact {
					text = attachments.ManagedSkillReference(name)
				}
				var content any = text
				if multipart {
					content = []map[string]string{{"type": "input_text", "text": text}}
				}
				request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON([]any{
					map[string]any{"role": "user", "content": content},
					map[string]any{"role": "user", "content": []map[string]string{{"type": "input_text", "text": attachments.Encode(frames)}}},
				})}}
				requestexecution.ProjectFileAttachments(&request)
				var messages []map[string]jsonv1.RawMessage
				if err := json.Unmarshal(request.Fields["input"], &messages); err != nil {
					t.Fatal(err)
				}
				want := 2
				if test.suppressed {
					want = 1
				}
				if len(messages) != want || !test.suppressed && !responses.SameJSONValue(messages[0]["content"], responses.MustMarshalJSON(content)) {
					t.Fatalf("wrong selected-skill suppression: %s", request.Fields["input"])
				}
				if !responses.SameJSONValue(messages[len(messages)-1]["content"], responses.MustMarshalJSON([]map[string]string{{"type": "input_text", "text": frames[0]}})) {
					t.Fatal("snapshot or omission notice changed")
				}
			})
		}
	}
}

func TestComposerFileAttachmentOversizedInputRestoresDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	d := conversationmodel.Input{Text: strings.Repeat("x", codexresponses.UserTextLimit), Attachments: []string{attachments.Encode([]string{"Attached file data"})}}
	if err := u.send([]conversationmodel.Input{d}, false); err != nil {
		t.Fatal(err)
	}
	if w.Len() != 0 || u.Text != d.Text || !u.feedback.Alert || u.Starting() {
		t.Fatal("oversized input was sent or draft lost")
	}
}

func TestComposerFileAttachmentLeavesOrdinaryUnicodeLimitToCodex(t *testing.T) {
	u, w := newAppServerTestUI()
	d := conversationmodel.Input{Text: strings.Repeat("界", 400_000)}
	if err := u.send([]conversationmodel.Input{d}, false); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || requests[0].text() != d.Text {
		t.Fatal("attachment safety limit rejected an ordinary Unicode prompt")
	}
}
