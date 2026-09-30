package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
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
	u.draft = "Review "
	bindComposerFile(u, `@"file name.go"`, "file name.go")
	u.draft += " please"
	draft := u.takeDraft()
	if err := os.WriteFile(path, []byte("changed after queueing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := u.send([]composerDraft{draft}, false); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || len(requests[0].Params.Input) != 2 {
		t.Fatalf("expected prompt and attachment input: %+v", requests)
	}
	input := requests[0].Params.Input
	if input[0].Text != draft.text {
		t.Fatalf("changed inline placeholder: %q", input[0].Text)
	}
	frames, ok := decodeFileAttachments(input[1].Text)
	if !ok || len(frames) != 1 || !strings.Contains(frames[0], attachmentReadGuidance) || !strings.HasSuffix(frames[0], "package example\n// original\n") {
		t.Fatalf("snapshot lost: %q", frames)
	}
	content, _ := json.Marshal(input)
	if text, _, ok := appServerUserText(content); !ok || text != draft.text {
		t.Fatalf("transport envelope leaked into transcript: %q", text)
	}
	resumed, _ := newAppServerTestUI()
	resumed.agents = newLiveActivityView()
	resumed.session.start(resumed.thread, cwd)
	resumed.restoreHistory([]appServerHistoryTurn{{ID: "saved", Status: "completed", Items: []appServerItem{{Type: "userMessage", ID: "saved-input", Content: content}}}})
	if len(resumed.inputHistory) != 1 || resumed.inputHistory[0].text != draft.text {
		t.Fatal("resume leaked attachment framing into recalled prompt")
	}
	providerParts := []map[string]any{{"type": "input_text", "text": input[0].Text}, {"type": "input_image", "image_url": "data:image/png;base64,test"}, {"type": "input_text", "text": input[1].Text}}
	raw := mustMarshalJSON([]any{map[string]any{"type": "message", "role": "user", "content": providerParts}})
	if question := journalQuestionFromInput(raw, "/root"); question != draft.text {
		t.Fatalf("attachment became journal question: %q", question)
	}
	request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": raw}}
	projectFileAttachments(&request)
	var messages []struct {
		Role    string              `json:"role"`
		Content []map[string]string `json:"content"`
	}
	if err := json.Unmarshal(request.fields["input"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || len(messages[0].Content) != 2 || messages[0].Content[0]["text"] != draft.text || messages[0].Content[1]["type"] != "input_image" || messages[1].Role != "user" || messages[1].Content[0]["text"] != frames[0] {
		t.Fatalf("projection did not preserve prompt/media and separate file: %+v", messages)
	}
	first := bytes.Clone(request.fields["input"])
	projectFileAttachments(&request)
	if !bytes.Equal(first, request.fields["input"]) {
		t.Fatal("projection is not idempotent")
	}
	// A fresh request built only from persisted Codex input needs no live UI or file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replayed := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": raw}}
	projectFileAttachments(&replayed)
	if !sameJSONValue(first, replayed.fields["input"]) {
		t.Fatal("replay changed submitted snapshot")
	}
}

func TestComposerFileAttachmentWireAndProviderPrefix(t *testing.T) {
	request := modelTestRequest(t, "gpt-6-sol")
	raw := mustMarshalJSON([]any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": "Review @example"},
		map[string]any{"type": "input_text", "text": encodeFileAttachments([]string{"Attached file /work/example:\nexact content"})},
	}}})
	request.setInput(raw)
	attempt := newRequestAttempt(requestExecutor{provider: &serverFakeProvider{}}, t.Context(), t.Context(), request, http.Header{}, "")
	if err := attempt.prepareWire(); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Input []jsonv1.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(attempt.forwardBody, &sent); err != nil || len(sent.Input) != 2 {
		t.Fatalf("wire did not carry separate user messages: %v", err)
	}
	prefix, err := (providerHistory{confirmed: true}).append(sent.Input)
	if err != nil {
		t.Fatal(err)
	}
	for _, automatic := range []bool{false, true} {
		request.setInput(raw)
		request.fields["previous_response_id"] = mustMarshalJSON("parent")
		projectFileAttachments(&request)
		exchange := &webSocketExchange{parentID: "parent", automatic: automatic, history: &webSocketHistory{parent: &webSocketHistory{providerHistory: prefix}}}
		if err := exchange.reconcileProviderHistory(&request, mustMarshalJSON(request.fields)); err != nil {
			t.Fatal(err)
		}
		if request.cachedInput != 2 || request.rebaseInput {
			t.Fatal("replayed snapshot changed provider prefix")
		}
	}
}

func TestComposerFileAttachmentNoticeBudget(t *testing.T) {
	d := composerDraft{}
	for i := range 1200 {
		d.files = append(d.files, composerFile{path: strings.Repeat("missing", 25) + string(rune(0x4e00+i))})
	}
	d.snapshotFileAttachments(t.TempDir())
	if len(d.attachments) != 1 || len(d.attachments[0]) > fileAttachmentBudget {
		t.Fatal("omission notices exceeded attachment budget")
	}
	frames, ok := decodeFileAttachments(d.attachments[0])
	if !ok || !strings.Contains(frames[len(frames)-1], "remaining contents NOT ATTACHED") {
		t.Fatal("omitted references were not reported")
	}
}

func TestComposerFileAttachmentChunksAndBounds(t *testing.T) {
	for _, content := range []string{"", strings.Repeat("界", fileAttachmentChunk), strings.Repeat("row\r\n", 13000), strings.Repeat("\"\\\t", 10000)} {
		frames := frameComposerFile("/work/example", content)
		var recovered strings.Builder
		for _, frame := range frames {
			_, body, ok := strings.Cut(frame, ":\n")
			if !ok || len(body) > fileAttachmentChunk {
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
		"large":   bytes.Repeat([]byte("x"), fileAttachmentBudget),
		"escaped": bytes.Repeat([]byte("\x01"), fileAttachmentBudget/3),
	} {
		if err := os.WriteFile(filepath.Join(cwd, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := composerDraft{}
	for _, path := range []string{"empty", "empty", "binary", "invalid", "large", "escaped", "missing", "."} {
		d.files = append(d.files, composerFile{path: path})
	}
	d.snapshotFileAttachments(cwd)
	if len(d.attachments) != 1 || len(d.attachments[0]) > fileAttachmentBudget || d.attachmentNotice == "" {
		t.Fatal("attachment budget/notice missing")
	}
	frames, ok := decodeFileAttachments(d.attachments[0])
	if !ok || len(frames) != 7 {
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
	if _, err := readComposerFile("relative.txt"); err == nil {
		t.Fatal("relative file used process cwd")
	}
}

func TestComposerFileAttachmentProjectionLeavesLookalikes(t *testing.T) {
	for _, item := range []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "input_text", "text": encodeFileAttachments([]string{"Attached file data"})}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "quoted " + encodeFileAttachments([]string{"Attached file data"})}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": fileAttachmentPrefix + "invalid" + fileAttachmentSuffix}}},
	} {
		raw := mustMarshalJSON([]any{item})
		request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": raw}}
		projectFileAttachments(&request)
		if !bytes.Equal(raw, request.fields["input"]) {
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
				frames := frameComposerSkillFromPath(name, test.path, "snapshot instructions")
				if test.omitted {
					frames = []string{strings.Split(frames[0], " (UTF-8 bytes ")[0] + `: CONTENT NOT ATTACHED ("too large"). Read this separately if needed.`}
				}
				text := selected
				if test.compact {
					text = managedSkillReference(name)
				}
				var content any = text
				if multipart {
					content = []map[string]string{{"type": "input_text", "text": text}}
				}
				request := parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{"input": mustMarshalJSON([]any{
					map[string]any{"role": "user", "content": content},
					map[string]any{"role": "user", "content": []map[string]string{{"type": "input_text", "text": encodeFileAttachments(frames)}}},
				})}}
				projectFileAttachments(&request)
				var messages []map[string]jsonv1.RawMessage
				if err := json.Unmarshal(request.fields["input"], &messages); err != nil {
					t.Fatal(err)
				}
				want := 2
				if test.suppressed {
					want = 1
				}
				if len(messages) != want || !test.suppressed && !sameJSONValue(messages[0]["content"], mustMarshalJSON(content)) {
					t.Fatalf("wrong selected-skill suppression: %s", request.fields["input"])
				}
				if !sameJSONValue(messages[len(messages)-1]["content"], mustMarshalJSON([]map[string]string{{"type": "input_text", "text": frames[0]}})) {
					t.Fatal("snapshot or omission notice changed")
				}
			})
		}
	}
}

func TestComposerFileAttachmentOversizedInputRestoresDraft(t *testing.T) {
	u, w := newAppServerTestUI()
	d := composerDraft{text: strings.Repeat("x", composerTextLimit), attachments: []string{encodeFileAttachments([]string{"Attached file data"})}}
	if err := u.send([]composerDraft{d}, false); err != nil {
		t.Fatal(err)
	}
	if w.Len() != 0 || u.draft != d.text || !u.noticeAlert || u.starting {
		t.Fatal("oversized input was sent or draft lost")
	}
}

func TestComposerFileAttachmentLeavesOrdinaryUnicodeLimitToCodex(t *testing.T) {
	u, w := newAppServerTestUI()
	d := composerDraft{text: strings.Repeat("界", 400_000)}
	if err := u.send([]composerDraft{d}, false); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || requests[0].text() != d.text {
		t.Fatal("attachment safety limit rejected an ordinary Unicode prompt")
	}
}
