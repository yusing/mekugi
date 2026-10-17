package executionobservation

import (
	"encoding/json"

	"strings"

	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestCodeModeReturnedNothing(t *testing.T) {
	header := "Script completed\nWall time 0.1 seconds\nOutput:\n"
	for raw, want := range map[string]bool{
		string(responses.MustMarshalJSON(header)):                                                                                                   true,
		string(responses.MustMarshalJSON(strings.TrimSuffix(header, "\n"))):                                                                         true,
		string(responses.MustMarshalJSON(header + "  \n")):                                                                                          true,
		string(responses.MustMarshalJSON(header + "printed")):                                                                                       false,
		`[{"type":"input_text","text":` + string(responses.MustMarshalJSON(header)) + `},{"type":"input_text","text":""}]`:                          true,
		`[{"type":"input_text","text":` + string(responses.MustMarshalJSON(header)) + `},{"type":"input_text","text":"x"}]`:                         false,
		`[{"type":"input_text","text":` + string(responses.MustMarshalJSON(header)) + `},{"type":"input_image","image_url":""}]`:                    false,
		`[{"type":"input_text","text":"Script failed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nTypeError"}]`:  true,
		`[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"},{"type":"input_text","text":"Script error:\nprinted"}]`: false,
		string(responses.MustMarshalJSON("Script running with cell ID 7\nWall time 10.0 seconds\nOutput:\n")):                                       false,
	} {
		if got := codeModeReturnedNothing(json.RawMessage(raw)); got != want {
			t.Errorf("codeModeReturnedNothing(%s) = %v, want %v", raw, got, want)
		}
	}
}
