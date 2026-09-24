package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestBufferedAnswerSSEFraming(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, stop := range []string{"completed", "failed", "eof", "read_error", "transform_error"} {
			t.Run(stop+"/"+map[string]string{"\n": "lf", "\r\n": "crlf"}[ending], func(t *testing.T) {
				transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
				answer := finalAnswerTestEvents(t, "final_answer")
				var wire strings.Builder
				for i, payload := range answer {
					var pretty bytes.Buffer
					if err := json.Indent(&pretty, payload, "", "  "); err != nil {
						t.Fatal(err)
					}
					answer[i] = bytes.Clone(pretty.Bytes())
					var envelope struct {
						Type string `json:"type"`
					}
					if err := json.Unmarshal(payload, &envelope); err != nil {
						t.Fatal(err)
					}
					wire.WriteString("event: " + envelope.Type + ending)
					for line := range strings.SplitSeq(pretty.String(), "\n") {
						wire.WriteString("data: " + line + ending)
					}
					wire.WriteString(ending)
				}
				expected := slices.Clone(answer)
				switch stop {
				case "completed", "failed":
					terminal := finalAnswerTestTerminal(t, stop, false)
					if stop == "completed" {
						var completed struct {
							Item json.RawMessage `json:"item"`
						}
						if err := json.Unmarshal(answer[len(answer)-1], &completed); err != nil {
							t.Fatal(err)
						}
						terminal = mustTestJSON(t, map[string]any{
							"type":     "response.completed",
							"response": map[string]any{"id": "response", "status": stop, "output": []json.RawMessage{completed.Item}},
						})
					}
					wire.WriteString("event: response." + stop + ending + "data: " + string(terminal) + ending + ending)
					expected = append(expected, terminal)
				case "transform_error":
					wire.WriteString("event: response.completed" + ending + `data: {"type":"response.completed","response":null}` + ending + ending)
				}
				disconnected := errors.New("upstream disconnected")
				var reader io.Reader = strings.NewReader(wire.String())
				if stop == "read_error" {
					reader = io.MultiReader(reader, finalAnswerErrorReader{disconnected})
				}
				var output bytes.Buffer
				chain := composeResponseTransformers(transform, &criticalErrorTransform{})
				_, err := copySSETransformed(&output, reader, chain, &responseHooks{onUsage: transform.observeResponseUsage})
				switch stop {
				case "read_error":
					if !errors.Is(err, disconnected) {
						t.Fatalf("upstream error changed: %v", err)
					}
				case "transform_error":
					if !errors.Is(err, errResponseTransform) {
						t.Fatalf("transform error changed: %v", err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
				// Parse physical SSE lines, not the JSON type alone: a consumer
				// listening for named events must receive every complete payload.
				frames := strings.Split(strings.TrimSuffix(strings.ReplaceAll(output.String(), "\r\n", "\n"), "\n\n"), "\n\n")
				if stop == "completed" {
					if len(frames) != 3 {
						t.Fatalf("completed final answer produced %d frames, want usage, journal flush, terminal: %s", len(frames), output.String())
					}
					for index, frame := range frames {
						lines := strings.Split(frame, "\n")
						payload := ssePayload(lines)
						var event struct {
							Type     string                     `json:"type"`
							Item     map[string]json.RawMessage `json:"item"`
							Response struct {
								Output []map[string]json.RawMessage `json:"output"`
							} `json:"response"`
						}
						if err := json.Unmarshal(payload, &event); err != nil {
							t.Fatal(err)
						}
						if lines[0] != "event: "+event.Type {
							t.Fatalf("event name lost: %q", lines[0])
						}
						for _, line := range lines[1:] {
							if !strings.HasPrefix(line, "data: ") {
								t.Fatalf("unframed payload line: %q", line)
							}
						}
						if index < 2 {
							if event.Type != "response.output_item.done" {
								t.Fatalf("frame %d type=%s, want output_item.done", index, event.Type)
							}
							text := commentaryMessageText(event.Item)
							if index == 0 && !strings.Contains(text, "Router session usage") {
								t.Fatalf("first terminal message is not usage: %s", text)
							}
							if index == 1 && (!strings.Contains(text, "Journal flush") || !strings.Contains(text, "**Question:**") ||
								!strings.Contains(text, "**Answer:**") || !strings.Contains(text, "No files were changed.")) {
								t.Fatalf("final answer was not rendered as a Question/Answer flush: %s", text)
							}
						} else if event.Type != "response.completed" || len(event.Response.Output) != 2 ||
							jsonString(event.Response.Output[0], "id") == "answer" || jsonString(event.Response.Output[1], "id") == "answer" {
							t.Fatalf("terminal snapshot did not retain only usage and journal messages: %s", payload)
						}
					}
				} else {
					if len(frames) != len(expected) {
						t.Fatalf("wrong frame count: %s", output.String())
					}
					for i, frame := range frames {
						lines := strings.Split(frame, "\n")
						payload := ssePayload(lines)
						if !bytes.Equal(payload, expected[i]) {
							t.Fatalf("payload %d changed: %q, want %q", i, payload, expected[i])
						}
						var envelope struct {
							Type string `json:"type"`
						}
						if err := json.Unmarshal(payload, &envelope); err != nil {
							t.Fatal(err)
						}
						if lines[0] != "event: "+envelope.Type {
							t.Fatalf("event name lost: %q", lines[0])
						}
						for _, line := range lines[1:] {
							if !strings.HasPrefix(line, "data: ") {
								t.Fatalf("unframed payload line: %q", line)
							}
						}
					}
				}
			})
		}
	}
}
func TestEncodeSSEMultilinePayloadPreservesFields(t *testing.T) {
	payload := []byte("{\n  \"type\": \"response.in_progress\"\n}")
	for _, ending := range []string{"\n", "\r\n", ""} {
		t.Run(map[string]string{"\n": "lf", "\r\n": "crlf", "": "unterminated"}[ending], func(t *testing.T) {
			fields := "event: response.in_progress\nid: provider-id\nretry: 1000\n: keep comment\n"
			lines := strings.SplitAfter(fields, "\n")
			lines = append(lines[:len(lines)-1], "data: old"+ending)
			encoded := encodeSSEEventPayload(lines, payload)
			if !strings.HasPrefix(encoded, fields) {
				t.Fatal("non-data fields changed")
			}
			recovered := ssePayload(strings.SplitAfter(encoded, "\n"))
			if !bytes.Equal(recovered, payload) {
				t.Fatalf("multiline payload changed: %q", recovered)
			}
			if ending == "" && strings.HasSuffix(encoded, "\n") {
				t.Fatal("unterminated frame acquired a final newline")
			}
		})
	}
}
