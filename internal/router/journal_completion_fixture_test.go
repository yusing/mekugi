package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
)

// finishTestResponse gives an otherwise terminal provider fixture an explicit
// finish while preserving its usage and substantive output.
func finishTestResponse(t *testing.T, response *http.Response) *http.Response {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	addFinish := func(payload []byte) []byte {
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		var output []jsontext.Value
		if raw := fields["output"]; len(raw) != 0 {
			if err := json.Unmarshal(raw, &output); err != nil {
				t.Fatal(err)
			}
		}
		output = append(output, mustMarshalJSON(journalFinishCall(`{"op":"finish"}`)))
		fields["output"] = mustMarshalJSON(output)
		return mustMarshalJSON(fields)
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		lines := strings.Split(string(body), "\n")
		for i, line := range lines {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok || !strings.Contains(data, `"response.completed"`) {
				continue
			}
			var event map[string]jsontext.Value
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				t.Fatal(err)
			}
			event["response"] = addFinish(event["response"])
			lines[i] = "data: " + string(mustMarshalJSON(event))
		}
		body = []byte(strings.Join(lines, "\n"))
	} else {
		body = addFinish(body)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response
}

func prepareCommentaryActivityTest(t *testing.T, proxy *mekugiProxy, session, thread, parent, name string, input []any) (*mekugiResponseTransform, *parsedResponsesRequest) {
	t.Helper()
	transform, request := prepareActivityTest(t, proxy, session, thread, parent, name, input)
	// These unit fixtures isolate commentary rendering; completion is covered by
	// journal-enabled integration tests.
	transform.journalActive = false
	return transform, request
}

// A completed host call keeps cache/metadata fixtures in the normal host loop.
func socketHostCallResponse(id string) map[string]any {
	return map[string]any{"type": "response.completed", "response": map[string]any{
		"id": id, "status": "completed", "output": []any{map[string]any{
			"type": "function_call", "id": id + "-item", "call_id": id + "-call", "name": "lookup", "arguments": "{}", "status": "completed",
		}},
	}}
}
