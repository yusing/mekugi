package outputdeduplication_test

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

type item = map[string]any

func registeredProjection(t *testing.T) binding.InputProjection {
	t.Helper()
	script := new(moduletest.Scripted)
	moduletest.New(t, outputdeduplication.Module, outputdeduplication.Deps{}, module.Slots{}, script)
	projections := script.InputProjections()
	if len(projections) != 1 || projections[0].Name != "outputdeduplication" {
		t.Fatalf("registered projections = %+v", projections)
	}
	return projections[0]
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decode(t *testing.T, raw []byte) []item {
	t.Helper()
	var items []item
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return items
}

func block(label string) string {
	return strings.Repeat(label+strings.Repeat("x", 96)+"\n", 3)
}

func tool(id, text string) item {
	return item{"type": "function_call_output", "call_id": id, "output": text}
}

func textPart(text string) item { return item{"type": "input_text", "text": text} }

func TestAcceptanceVisibleSourcesAndReceipts(t *testing.T) {
	p := registeredProjection(t)
	a, b, c := block("alpha"), block("beta"), block("gamma")
	input := encode(t, []item{tool("source", b), tool("partial", a+b+c), tool("repeat", a+b+c)})
	result := p.Prepare(input)(input)
	got := decode(t, result.Input)
	want := []string{"O1:\n" + b, "O2:\n" + a + "See O1\n" + c,
		"See O2:1-3\nSee O1\nSee O2:5-7\n"}
	for i, text := range want {
		if got[i]["output"] != text {
			t.Fatalf("output %d = %q, want %q", i, got[i]["output"], text)
		}
	}
	if result.Receipt == nil || result.Receipt.OutputRepetitions != 2 ||
		!reflect.DeepEqual(result.Receipt.RepeatedCalls, map[string]string{"partial": "3 lines repeated", "repeat": "repeated"}) {
		t.Fatalf("receipt = %+v", result.Receipt)
	}
	prefix := encode(t, decode(t, input)[:2])
	if earlier := decode(t, p.Prepare(prefix)(prefix).Input); !reflect.DeepEqual(earlier, got[:2]) {
		t.Fatal("extending the request changed an earlier projected item")
	}
	if again := p.Prepare(input)(input); !bytes.Equal(again.Input, result.Input) || !reflect.DeepEqual(again.Receipt, result.Receipt) {
		t.Fatal("fresh preparation changed projection or receipt")
	}
	isolated := encode(t, []item{tool("new-view", b)})
	if got := decode(t, p.Prepare(isolated)(isolated).Input)[0]["output"]; got != "O1:\n"+b {
		t.Fatalf("fresh view retained an earlier request source: %q", got)
	}
}

func TestAcceptanceOriginalPartBounds(t *testing.T) {
	p := registeredProjection(t)
	body := block("original host output")
	header := "Chunk ID: xyz\nWall time: 0.2 seconds\nProcess exited with code 0\nFinal output:\n"
	for _, originalOutput := range []any{header + body, []item{textPart(header + body)}} {
		original := encode(t, []item{tool("source", body), {"type": "custom_tool_call_output", "call_id": "repeat", "output": originalOutput}})
		prepared := p.Prepare(original)
		final := decode(t, original)
		final[1]["output"] = []item{textPart(header + body), textPart(body)}
		input := encode(t, final)
		unchanged := bytes.Clone(input)
		result := prepared(input)
		parts := decode(t, result.Input)[1]["output"].([]any)
		if parts[0].(map[string]any)["text"] != header+"See O1\n" || parts[1].(map[string]any)["text"] != body {
			t.Fatalf("original/appended parts = %+v", parts)
		}
		if result.Receipt == nil || result.Receipt.OutputRepetitions != 1 || result.Receipt.RepeatedCalls["repeat"] != "repeated" {
			t.Fatalf("receipt = %+v", result.Receipt)
		}
		if !bytes.Equal(unchanged, input) {
			t.Fatal("projection mutated its final-input copy")
		}
	}
}

func TestAcceptanceDurableAttachmentBodies(t *testing.T) {
	p := registeredProjection(t)
	body := block("submitted snapshot")
	fileHeader := fmt.Sprintf("Attached file %q (UTF-8 bytes 0:%d of %d; %s):\n", "/never-open/snapshot.txt", len(body), len(body), attachments.ReadGuidance)
	skillHeader := fmt.Sprintf("Attached skill %q from %q (UTF-8 bytes 0:%d of %d; %s):\n", "snapshot", "/never-open/SKILL.md", len(body), len(body), attachments.ReadGuidance)
	envelope := attachments.Encode([]string{fileHeader + body, skillHeader + body})
	input := encode(t, []item{{"role": "user", "content": []item{textPart(envelope)}}, tool("tool-repeat", body)})
	result := p.Prepare(input)(input)
	got := decode(t, result.Input)
	projected := got[0]["content"].([]any)[0].(map[string]any)["text"].(string)
	frames, ok := attachments.Decode(projected)
	if !ok || !reflect.DeepEqual(frames, []string{fileHeader + "O1:\n" + body, skillHeader + "See O1\n"}) {
		t.Fatalf("projected attachment frames = %q, complete = %v", frames, ok)
	}
	if got[1]["output"] != "See O1\n" || result.Receipt == nil || result.Receipt.OutputRepetitions != 2 ||
		!reflect.DeepEqual(result.Receipt.RepeatedCalls, map[string]string{"tool-repeat": "repeated"}) || len(result.Receipt.RepeatedAttachments) != 0 {
		t.Fatalf("tool = %+v, receipt = %+v", got[1], result.Receipt)
	}
}

func TestAcceptanceTokenGateKeepsLiteralSources(t *testing.T) {
	p := registeredProjection(t)
	a, b := strings.Repeat("x", 192)+"\n", strings.Repeat("x", 192)+"y\n"
	valuable := strings.Repeat("valuable output content ", 40) + "\n"
	body := a + b + valuable
	header := fmt.Sprintf("Attached file %q (UTF-8 bytes 0:%d of %d; %s):\n", "/never-open/gate.txt", len(body), len(body), attachments.ReadGuidance)
	envelope := (attachments.Snapshot{ID: "gate", Frames: []string{header + body}}).Encode()
	items := []item{tool("valuable", valuable), tool("a", a), tool("b", b),
		{"role": "user", "content": []item{textPart(envelope)}}, tool("combined", a+b)}
	input := encode(t, items)
	original := bytes.Clone(input)
	result := p.Prepare(input)(input)
	got := decode(t, result.Input)
	projected := got[3]["content"].([]any)[0].(map[string]any)["text"].(string)
	frames, ok := attachments.Decode(projected)
	if !ok || !reflect.DeepEqual(frames, []string{header + "O2:\n" + a + b + "See O1\n"}) ||
		got[4]["output"] != "See O2:1-2\n" {
		t.Fatalf("rejected runs lost literal rows or referenceability: frames=%q final=%q", frames, got[4]["output"])
	}
	if result.Receipt.OutputRepetitions != 2 || len(result.Receipt.RepeatedAttachments) != 1 ||
		!reflect.DeepEqual(result.Receipt.RepeatedCalls, map[string]string{"combined": "repeated"}) {
		t.Fatalf("receipt = %+v", result.Receipt)
	}
	prefix := encode(t, items[:4])
	if earlier := decode(t, p.Prepare(prefix)(prefix).Input); !reflect.DeepEqual(earlier, got[:4]) {
		t.Fatal("extending rejected runs changed an earlier projection")
	}
	if !bytes.Equal(original, input) {
		t.Fatal("projection mutated original evidence")
	}
}

func TestAcceptanceUnsupportedInputPassesThrough(t *testing.T) {
	p := registeredProjection(t)
	body := block("excluded")
	frame := "Attached file \"/missing\" (UTF-8 bytes 0:999 of 999; " + attachments.ReadGuidance + "):\n" + body
	envelope := attachments.Encode([]string{frame})
	for name, candidate := range map[string]item{
		"no call identity":    {"type": "function_call_output", "output": body},
		"media":               {"type": "function_call_output", "call_id": "media", "output": []item{{"type": "image", "url": body}}},
		"ordinary prompt":     {"role": "user", "content": []item{textPart(body)}},
		"bare frame":          {"role": "user", "content": []item{textPart(frame)}},
		"string envelope":     {"role": "user", "content": envelope},
		"non-input envelope":  {"role": "user", "content": []item{{"type": "text", "text": envelope}}},
		"incomplete envelope": {"role": "user", "content": []item{textPart(strings.TrimSuffix(envelope, attachments.Suffix))}},
		"selection":           {"role": "user", "content": []item{textPart(attachments.Encode([]string{"Selected text for \"selection\":\n" + body}))}},
		"base instructions":   {"role": "developer", "content": []item{textPart(envelope)}},
	} {
		t.Run(name, func(t *testing.T) {
			input := encode(t, []item{tool("seed", body), candidate})
			result := p.Prepare(input)(input)
			if got := decode(t, result.Input)[1]; !reflect.DeepEqual(got, decode(t, input)[1]) {
				t.Fatalf("unsupported item changed: %+v", got)
			}
			if result.Receipt == nil || result.Receipt.OutputRepetitions != 0 || len(result.Receipt.RepeatedCalls) != 0 {
				t.Fatalf("unsupported item receipt = %+v", result.Receipt)
			}
		})
	}
	for _, input := range [][]byte{[]byte(`[{`), []byte(`{"input":[]}`)} {
		if got := p.Prepare(input)(input); !bytes.Equal(got.Input, input) {
			t.Fatalf("malformed input changed: %s", got.Input)
		}
	}
}
