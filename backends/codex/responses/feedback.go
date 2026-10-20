package responses

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// AppendApprovalFeedback projects already-delivered control feedback without
// issuing a host steer or changing its answer.
// Source: apps/mekugi/internal/router/mekugi_proxy.go:528:542@d002a0dc.
func (r *Request) AppendApprovalFeedback(feedback string) error {
	if feedback == "" {
		return nil
	}
	var input []jsonv1.RawMessage
	if err := json.Unmarshal(r.Fields["input"], &input); err != nil {
		return fmt.Errorf("read input for denial feedback: %w", err)
	}
	input = append(input, responses.MustMarshalJSON(map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": feedback}}}))
	r.Fields["input"] = responses.MustMarshalJSON(input)
	return nil
}
