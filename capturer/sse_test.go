package capturer

import (
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
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

func TestSSEObservationAndMeasurementUseIdenticalFraming(t *testing.T) {
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
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
			wantOutput := string(observer([]byte(test.stream), "text/event-stream", &baseline, codec))
			wantTokens, err := contentTokens([]byte(test.stream), codec)
			if err != nil || wantTokens == 0 || baseline.ResponseStatus != "completed" || wantOutput == "" {
				t.Fatalf("invalid test baseline: tokens=%d status=%q output=%q err=%v", wantTokens, baseline.ResponseStatus, wantOutput, err)
			}
			for _, ending := range []string{"\n", "\r\n", "\r"} {
				for _, bom := range []string{"", "\xef\xbb\xbf"} {
					payload := []byte(bom + strings.ReplaceAll(test.stream, "\n", ending))
					for _, contentType := range []string{"text/event-stream", ""} {
						var record captureRecord
						output := observer(payload, contentType, &record, codec)
						tokens, err := contentTokens(payload, codec)
						if err != nil || tokens != wantTokens || string(output) != wantOutput || record.ResponseStatus != "completed" || record.CaptureError != "" {
							t.Fatalf("ending=%q BOM=%q type=%q: tokens=%d want=%d status=%q error=%q output=%q err=%v", ending, bom, contentType, tokens, wantTokens, record.ResponseStatus, record.CaptureError, output, err)
						}
					}
				}
			}
		})
	}
}
