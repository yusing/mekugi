package executionobservation

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/outputdeduplication"
)

func TestPreparedContentRetainsActualDeduplicationAndWholeCell(t *testing.T) {
	f := newNativeTraceFixture(t)
	f.start("thread", "runtime", "cell", "source")
	for _, id := range []string{"one", "two"} {
		f.tool("thread", "runtime", id, "exec_command", `{"cmd":"echo text"}`)
	}
	f.end("thread", "runtime")
	audience := sessioncontrol.NewAudience(nil)
	audience.Attach("thread")
	body := strings.Repeat("distinctive repeated file content\n", 40)
	frame := "Attached file \"file.txt\" (UTF-8 bytes 0:1280 of 1280; " + attachments.ReadGuidance + "):\n" + body
	first := attachments.Snapshot{ID: "first", Frames: []string{frame}}
	second := attachments.Snapshot{ID: "second", Frames: []string{frame}}
	input := []any{
		map[string]any{"type": "custom_tool_call", "call_id": "cell", "name": "exec", "input": "source"},
		map[string]any{"type": "custom_tool_call_output", "call_id": "cell", "output": "actual whole-cell output"},
	}
	for _, snapshot := range []attachments.Snapshot{first, second} {
		input = append(input, map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": snapshot.Encode()}}})
	}
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"input": input, "model": "gpt-test"}))
	if err != nil {
		t.Fatal(err)
	}
	original := string(request.OriginalField("input"))
	service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	projection := outputdeduplication.Module.Bindings.InputProjection(service).Prepare(request.Fields["input"])
	request.Fields["input"] = projection(request.Fields["input"]).Input
	_, ids := requestexecution.ProjectFileAttachments(&request)
	transform := mekugiResponseTransform{activity: audience, nativeTrace: nativetrace.New(f.root), threadID: "thread", codeModeToolName: "exec"}
	transform.ObservePreparedContent(&request, ids)
	got, ok := audience.Prepared("thread", "thread", second.ReceiptID(frame))
	var finalItems []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &finalItems); err != nil {
		t.Fatal(err)
	}
	var expected []string
	for index, id := range ids {
		if id != second.ReceiptID(frame) {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(finalItems[index]["content"], &parts); err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			expected = append(expected, part.Text)
		}
	}
	if !ok || !strings.Contains(got.Text, "[same as") || strings.Contains(got.Text, body) || got.Text != strings.Join(expected, "\n\n") {
		t.Fatalf("actual deduplication missing: %+v", got)
	}
	for _, id := range []string{"one", "two"} {
		got, ok := audience.Prepared("thread", "thread", id)
		if !ok || got.ID != "cell" || !got.Cell || got.Text != "actual whole-cell output" {
			t.Fatalf("whole-cell identity for %s: %+v", id, got)
		}
	}
	if string(request.OriginalField("input")) != original {
		t.Fatal("original changed")
	}
}
