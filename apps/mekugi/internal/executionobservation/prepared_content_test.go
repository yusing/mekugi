package executionobservation

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestPreparedContentReadableComparison(t *testing.T) {
	for _, tc := range []struct {
		name, original, prepared, want string
		altered                        bool
	}{
		{"equal string", `"hello"`, `"hello"`, "hello", false},
		{"equal wrapper", `"hello"`, `[{"type":"input_text","text":"hello"}]`, "hello", false},
		{"altered wrapper", `[{"type":"input_text","text":"hello"}]`, `[{"type":"input_text","text":"[same as O1]"}]`, "[same as O1]", true},
		{"multipart", `[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]`, `[{"type":"input_text","text":"a"},{"type":"input_text","text":"c"}]`, "a\nc", true},
		{"mixed media", `[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"kept"}]`, `[{"type":"input_text","text":"b"},{"type":"input_image","image_url":"kept"}]`, "b\n" + `{"type":"input_image","image_url":"kept"}`, true},
		{"structured array", `[1,2]`, `[1,3]`, `[1,3]`, true},
		{"media array", `[{"type":"input_image","image_url":"a"}]`, `[{"type":"input_image","image_url":"b"}]`, `[{"type":"input_image","image_url":"b"}]`, true},
		{"structured", `{"text":"original","data":1}`, `{"text":"changed","data":2}`, `{"text":"changed","data":2}`, true},
		{"extended text", `"a"`, `[{"type":"input_text","text":"b","metadata":1}]`, `[{"type":"input_text","text":"b","metadata":1}]`, true},
		{"printed JSON", `"old"`, `"{\"text\":\"literal\"}"`, `{"text":"literal"}`, true},
		{"missing original", ``, `[{"type":"input_text","text":"new"}]`, "new", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audience := sessioncontrol.NewAudience(nil)
			audience.Attach("thread")
			original := `[]`
			if tc.original != "" {
				original = `[{"type":"function_call_output","call_id":"call","output":` + tc.original + `}]`
			}
			request, err := codexresponses.ParseRequest([]byte(`{"model":"test","input":` + original + `}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Fields["input"] = []byte(`[{"type":"function_call_output","call_id":"call","output":` + tc.prepared + `}]`)
			transform := mekugiResponseTransform{owners: &Preparer{Config: Config{Audience: audience}}, threadID: "thread"}
			transform.ObservePreparedContent(&request, nil)
			got, ok := audience.Prepared("thread", "thread", "call")
			if !ok || got.Text != tc.want || got.Altered != tc.altered {
				t.Fatalf("got %+v, want text %q altered %v", got, tc.want, tc.altered)
			}
		})
	}
}
