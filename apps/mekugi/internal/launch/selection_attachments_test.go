package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/testsupport/moduletest"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestSelectionLargeDiffFenceSurvivesModelProjection(t *testing.T) {
	const label = "[Selected diff]"
	body := strings.Repeat("`", attachments.Chunk)
	draft := composer.Draft{Text: label, Selections: []composer.Token{{Start: 0, End: len(label), Text: body}}}
	draft.Attachments = []string{attachments.Encode(moduletest.SelectionFrames(draft.Text[draft.Selections[0].Start:draft.Selections[0].End], draft.Selections[0].Source, draft.Selections[0].Text))}
	if err := moduletest.IdentifyAttachments(&draft.Attachments, "selection"); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := attachments.DecodeSnapshot(draft.Attachments[0])
	if !ok {
		t.Fatal("selection producer emitted an invalid envelope")
	}
	file := moduletest.FileFrames("/file.txt", "FILE_BODY")[0]
	skill := moduletest.SkillFrames("guide", "", "SKILL_BODY")[0]
	snapshot.Frames = append(snapshot.Frames, file, skill)
	input := []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": snapshot.Encode()}}}}
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"input": input}))
	if err != nil {
		t.Fatal(err)
	}
	changed, ids := requestexecution.ProjectFileAttachments(&request)
	if !changed || len(ids) != 3 || ids[0] != snapshot.ReceiptID(snapshot.Frames[0]) || strings.Contains(string(request.Fields["input"]), "mekugi-file-attachments") {
		t.Fatal("projected mixed envelope did not expand")
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expanded messages: %d", len(items))
	}
	var frames []string
	for _, item := range items {
		var content []map[string]string
		if err := json.Unmarshal(item["content"], &content); err != nil || len(content) != 1 {
			t.Fatalf("projected content: %s, %v", item["content"], err)
		}
		frames = append(frames, content[0]["text"])
	}
	selection, ok := attachments.ParseSelection(frames[0])
	if !ok || selection.Body != body || !strings.HasSuffix(selection.Footer, "</selected-text>") {
		t.Fatal("large diff fence or source bytes lost after attachment projection")
	}
	for i, original := range []string{file, skill} {
		header, body, _ := strings.Cut(original, "\n")
		if frames[i+1] != fmt.Sprintf("%s\n%s", header, body) {
			t.Fatalf("file or skill framing changed: %q", frames[i+1])
		}
	}
}
