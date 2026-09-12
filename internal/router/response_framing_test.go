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
				_, err := copySSETransformed(&output, reader, chain, transform.observeResponseUsage)
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
