package capture

import (
	"slices"
	"strings"
	"testing"
)

func TestSSEDataPreservesFieldsAcrossFraming(t *testing.T) {
	const stream = ": comment\nevent: message\nid: ignored\ndata:  leading \ndata: trailing  \n\ndata\n\ndata: final"
	want := []string{" leading \ntrailing  ", "", "final"}
	for _, ending := range []string{"\n", "\r\n", "\r"} {
		for _, bom := range []string{"", "\xef\xbb\xbf"} {
			payload := []byte(bom + strings.ReplaceAll(stream, "\n", ending))
			var got []string
			for data := range sseData(payload) {
				got = append(got, string(data))
			}
			if !slices.Equal(got, want) {
				t.Fatalf("ending=%q BOM=%q: %q, want %q", ending, bom, got, want)
			}
			for data := range sseData(payload) {
				if string(data) != want[0] {
					t.Fatalf("early stop: %q", data)
				}
				break
			}
		}
	}
}

func TestSSEObservationPreservesFraming(t *testing.T) {
	for _, test := range []struct {
		name   string
		stream string
		chat   bool
	}{
		{
			name:   "Responses",
			stream: "event: response.completed\ndata: {\"type\":\"response.completed\",\ndata: \"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\ndata: [DONE]\n\n",
		},
		{
			name:   "Chat",
			stream: "event: message\ndata: {\"choices\":[\ndata: {\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			chat:   true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			observer := observeResponse
			if test.chat {
				observer = observeChatResponse
			}
			var baseline captureRecord
			observer([]byte(test.stream), "text/event-stream", &baseline)
			if baseline.ResponseStatus != "completed" {
				t.Fatalf("invalid baseline status=%q", baseline.ResponseStatus)
			}
			for _, ending := range []string{"\n", "\r\n", "\r"} {
				for _, bom := range []string{"", "\xef\xbb\xbf"} {
					payload := []byte(bom + strings.ReplaceAll(test.stream, "\n", ending))
					for _, contentType := range []string{"text/event-stream", ""} {
						var record captureRecord
						observer(payload, contentType, &record)
						if record.ResponseStatus != "completed" || record.CaptureError != "" {
							t.Fatalf("ending=%q BOM=%q type=%q: status=%q error=%q", ending, bom, contentType, record.ResponseStatus, record.CaptureError)
						}
					}
				}
			}
		})
	}
}
