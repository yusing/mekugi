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
	"github.com/yusing/mekugi/modules/conversation/composer"
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

func TestAcceptanceRepeatedMultilineOutput(t *testing.T) {
	p := registeredProjection(t)
	body := strings.Repeat("Measured output from an admitted child request.\n", 32)
	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprintf("multipart=%v", multipart), func(t *testing.T) {
			output := func(text string) any {
				if multipart {
					return []item{textPart(text)}
				}
				return text
			}
			input := encode(t, []item{
				{"type": "function_call_output", "call_id": "source", "output": output(body)},
				{"type": "custom_tool_call_output", "call_id": "repeat", "output": output(body)},
			})
			evidence := bytes.Clone(input)
			result := p.Prepare(input)(input)
			want := encode(t, []item{
				{"type": "function_call_output", "call_id": "source", "output": output("O1:\n" + body)},
				{"type": "custom_tool_call_output", "call_id": "repeat", "output": output("See O1\n")},
			})
			if !reflect.DeepEqual(decode(t, result.Input), decode(t, want)) || !bytes.Equal(input, evidence) {
				t.Fatalf("projection = %s, want %s; evidence preserved = %v", result.Input, want, bytes.Equal(input, evidence))
			}
			if result.Receipt == nil || result.Receipt.OutputRepetitions != 1 ||
				result.Receipt.RepeatedCalls["repeat"] != "repeated" || result.Receipt.SavedTokens <= 0 {
				t.Fatalf("receipt = %+v", result.Receipt)
			}
		})
	}
}

func TestAcceptanceInteriorRepeatedSeed(t *testing.T) {
	p := registeredProjection(t)
	line := strings.Repeat("Measured output from an admitted child request. ", 8) + "\n"
	source, target := line+"one\n"+line+"two\n"+line, "one\n"+line+"two\n"
	input := encode(t, []item{tool("source", source), tool("target", target)})
	result := p.Prepare(input)(input)
	if got := decode(t, result.Input)[1]["output"]; got != "See O1:2-4\n" ||
		result.Receipt == nil || result.Receipt.RepeatedCalls["target"] != "repeated" {
		t.Fatalf("interior repeated seed = %q, receipt = %+v", got, result.Receipt)
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

func TestAcceptanceChunkedSelectionAndWholeReload(t *testing.T) {
	body := "START_SENTINEL" + strings.Repeat("a", 30000) + "END_SENTINEL"
	draft := composer.Draft{Text: "[Selected diff]", Selections: []composer.Token{{Start: 0, End: len("[Selected diff]"), Text: body}}}
	draft.SnapshotFiles("")
	if err := draft.IdentifyAttachments("selection"); err != nil {
		t.Fatal(err)
	}
	for _, reloadFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(reloadFirst), func(t *testing.T) {
			p := registeredProjection(t)
			items := []item{{"role": "user", "content": []item{textPart(draft.Attachments[0])}}, tool("compact-reader", "'cat' '--' '/selected': exit_code=0\n"+body)}
			if reloadFirst {
				items[0], items[1] = items[1], items[0]
			}
			input := encode(t, items)
			result := p.Prepare(input)(input)
			for _, marker := range []string{"START_SENTINEL", "END_SENTINEL"} {
				if bytes.Count(result.Input, []byte(marker)) != 1 {
					t.Fatalf("chunked source repeated %s", marker)
				}
			}
			at := 0
			if reloadFirst {
				at = 1
			}
			projected := decode(t, result.Input)[at]["content"].([]any)[0].(map[string]any)["text"].(string)
			snapshot, ok := attachments.DecodeSnapshot(projected)
			if !ok {
				t.Fatal("projected chunks lost the supported envelope bounds")
			}
			for _, frame := range snapshot.Frames {
				selection, ok := attachments.ParseSelection(frame)
				if !ok || !strings.HasSuffix(selection.Header, "```diff\n") || selection.Footer != "\n```\n</selected-text>" {
					t.Fatalf("deduplicated diff lost its framing: %q", frame[:min(200, len(frame))])
				}
			}
		})
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

func TestAcceptanceSingleBaseEditsThroughHostAndAttachment(t *testing.T) {
	p := registeredProjection(t)
	var original, changed strings.Builder
	for i := range 40 {
		row := fmt.Sprintf("requirement %02d: keep original output evidence, direct references, and exact whitespace %s\n", i, strings.Repeat("important detail ", 5))
		original.WriteString(row)
		if i == 1 {
			row = strings.Replace(row, "original", "unchanged", 1)
		}
		changed.WriteString(row)
		if i%5 == 0 {
			changed.WriteString("    \n")
		}
	}
	base, body := original.String(), changed.String()
	header := fmt.Sprintf("Attached file %q (UTF-8 bytes 0:%d of %d; %s):\n", "/never-open/near.txt", len(body), len(body), attachments.ReadGuidance)
	envelope := (attachments.Snapshot{ID: "near", Frames: []string{header + body}}).Encode()
	input := encode(t, []item{tool("source", base), tool("changed", body), {"role": "user", "content": []item{textPart(envelope)}}})
	evidence := bytes.Clone(input)
	result := p.Prepare(input)(input)
	got := decode(t, result.Input)
	projected := got[1]["output"].(string)
	if !strings.HasPrefix(projected, "See O1 with byte edits") || strings.Contains(projected, "See O2") {
		t.Fatalf("host output = %q", projected)
	}
	frames, ok := attachments.Decode(got[2]["content"].([]any)[0].(map[string]any)["text"].(string))
	if !ok || len(frames) != 1 || frames[0] != header+projected {
		t.Fatalf("attachment projection = %q", frames)
	}
	if !bytes.Equal(input, evidence) || result.Receipt == nil || result.Receipt.OutputRepetitions != 2 ||
		result.Receipt.RepeatedCalls["changed"] != "39 lines repeated" || len(result.Receipt.RepeatedAttachments) != 1 ||
		!result.Receipt.TokensMeasured || result.Receipt.SavedTokens <= 0 {
		t.Fatalf("evidence or receipt changed: %+v", result.Receipt)
	}
	prefix := encode(t, decode(t, input)[:2])
	if earlier := decode(t, p.Prepare(prefix)(prefix).Input); !reflect.DeepEqual(earlier, got[:2]) {
		t.Fatal("appended attachment changed earlier host projection")
	}
}

func TestAcceptanceRecoveredAttachmentsAndExplicitReload(t *testing.T) {
	var draft composer.Draft
	draft.Skills = []composer.Token{{Name: "guide", Path: "/SKILL.md"}}
	draft.SnapshotSkills(func(_, _ string) ([]byte, error) { return []byte("SHORT_SKILL"), nil })
	skill := draft.Attachments[0]
	selection := attachments.Snapshot{ID: "submitted", Frames: []string{"Selected text for \"[Selected 1]\" (UTF-8 bytes 0:15 of 15):\nSHORT_SELECTION"}}.Encode()
	for _, prefix := range []string{"", "<mekugi-file-attachments-v0>\nmalformed\n"} {
		recovered := "[stdout maple bytes]\n" + prefix + selection + "\n[/stdout]\n[stdout birch bytes]\n" + skill + "\n[/stdout]"
		items := []item{tool("recall", recovered), tool("compact-skill", "'cat' '/SKILL.md': exit_code=0\nSHORT_SKILL"), tool("compact-selection", "'cat' '/selection': exit_code=0\nSHORT_SELECTION")}
		p := registeredProjection(t)
		input := encode(t, items)
		result := p.Prepare(input)(input)
		if bytes.Count(result.Input, []byte("SHORT_SKILL")) != 1 || bytes.Count(result.Input, []byte("SHORT_SELECTION")) != 1 {
			t.Fatalf("recovered envelopes failed shared source dedup: %s", result.Input)
		}
	}
}

func TestAcceptanceMalformedLegacySelectionRemainsLiteral(t *testing.T) {
	frame := "Selected text for \"[Selected message]\" (UTF-8 bytes not-a-range\nquoted bytes"
	input := encode(t, []item{{"role": "user", "content": []item{textPart(attachments.Encode([]string{frame}))}}})
	p := registeredProjection(t)
	if result := p.Prepare(input)(input); !bytes.Equal(result.Input, input) {
		t.Fatalf("malformed selection became a source: %s", result.Input)
	}
}

func TestAcceptanceSuppliedMessageSources(t *testing.T) {
	body := "short supplied bytes\r\n"
	message := func(body string) string {
		header, footer := "reader: exit_code=0\n", "\nreader diagnostic\n"
		return "Keep these instructions.\n" + attachments.SourceRanges(header+body+footer, []attachments.SourceRange{{Start: len(header), End: len(header) + len(body)}})
	}
	for _, kind := range []string{"agent_message", "multi_agent.inter_agent_message", "multi_agent.inter_agent_completion_message"} {
		t.Run(kind, func(t *testing.T) {
			carrier := item{"type": "message", "role": "assistant", "id": "threadcontrol_" + strings.Repeat("a", 64), "phase": "commentary",
				"content": []item{{"type": "output_text", "text": message(body)}},
				"internal_chat_message_metadata_passthrough": item{"content_item_kinds": []string{kind}}}
			if kind == "agent_message" {
				carrier = item{"type": "agent_message", "author": "/root", "recipient": "/root/worker", "content": []item{textPart(message(body))}}
			}
			input := encode(t, []item{
				carrier,
				{"role": "user", "content": []item{textPart("Side question\n" + attachments.SourceRanges(body, []attachments.SourceRange{{Start: 0, End: len(body)}}))}},
				{"type": "function_call_output", "namespace": "threadcontrol", "name": "followup_task", "output": message(body)},
				tool("compact-reload", "reader: exit_code=0\n"+body),
			})
			p := registeredProjection(t)
			result := p.Prepare(input)(input)
			got := decode(t, result.Input)
			first := got[0]["content"].([]any)[0].(map[string]any)["text"].(string)
			if first != message("O1:\n"+body) ||
				got[2]["output"] != message("See O1\n") ||
				got[3]["output"] != "reader: exit_code=0\nSee O1\n" {
				t.Fatalf("attributed source did not share the request index: %+v", got)
			}
			if result.Receipt.OutputRepetitions != 3 || !result.Receipt.TokensMeasured {
				t.Fatalf("source receipts missing: %+v", result.Receipt)
			}
			if !bytes.Equal(result.Input, p.Prepare(input)(input).Input) {
				t.Fatal("request-local projection changed across retries")
			}
			fresh := encode(t, decode(t, input)[1:2])
			if bytes.Contains(p.Prepare(fresh)(fresh).Input, []byte("See O")) {
				t.Fatal("source reference escaped its visible request")
			}
		})
	}
}

func TestAcceptanceSuppliedMessageExclusions(t *testing.T) {
	text := attachments.SourceRanges("source bytes", []attachments.SourceRange{{Start: 0, End: len("source bytes")}})
	for _, excluded := range []item{
		{"type": "agent_message", "recipient": "/root", "content": []item{textPart(text)}},
		{"type": "agent_message", "author": "/root", "content": []item{textPart(text)}},
		{"type": "agent_message", "author": "/root", "recipient": "/root/worker", "content": text},
		{"type": "agent_message", "author": "/root", "recipient": "/root/worker", "content": []item{{"type": "output_text", "text": text}}},
		{"type": "agent_message", "author": "/root", "recipient": "/root/worker", "content": []item{textPart(text), {"type": "encrypted_content", "encrypted_content": "opaque"}}},
		{"role": "developer", "content": []item{textPart(text)}},
		{"role": "assistant", "content": []item{{"type": "output_text", "text": text}}},
		{"role": "user", "content": text},
		{"role": "user", "content": []item{{"type": "input_image", "text": text}}},
		{"role": "user", "content": []item{textPart(text)}, "internal_chat_message_metadata_passthrough": item{"content_item_kinds": []string{"developer.instructions"}}},
		{"type": "function_call_output", "output": text},
		tool("malformed", strings.Replace(text, `bytes="12"`, `bytes="13"`, 1)),
	} {
		p := registeredProjection(t)
		input := encode(t, []item{excluded, excluded})
		if result := p.Prepare(input)(input); !bytes.Equal(input, result.Input) || result.Receipt.OutputRepetitions != 0 {
			t.Fatalf("excluded input changed: %s", result.Input)
		}
	}
}
