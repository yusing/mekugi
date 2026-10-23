package presentation

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/yusing/mekugi/contracts/observation"
)

func TestMCPDetailPreservesOpaqueJSON(t *testing.T) {
	for _, tt := range []struct {
		name, args, result, hostError, want string
	}{
		{
			name:      "exact bytes and large numbers",
			args:      ` { "model": "requested", "n": 9007199254740993 } `,
			result:    `{"content": [{"type":"text","text":"opaque"}], "n":18446744073709551615}`,
			hostError: `{"message": "failure", "n": 9007199254740995}`,
			want:      "{\n\"arguments\":  { \"model\": \"requested\", \"n\": 9007199254740993 } ,\n\"result\": {\"content\": [{\"type\":\"text\",\"text\":\"opaque\"}], \"n\":18446744073709551615},\n\"error\": {\"message\": \"failure\", \"n\": 9007199254740995}\n}",
		},
		{name: "empty"},
		{name: "null outcomes omitted", args: "null", result: "null", hostError: "null", want: "{\n\"arguments\": null\n}"},
		{name: "malformed payload retained", result: `{broken`, want: "{\n\"result\": {broken\n}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := observation.Item{Arguments: jsontext.Value(tt.args), Result: jsontext.Value(tt.result), Error: jsontext.Value(tt.hostError)}
			if got := MCPDetail(item); got != tt.want {
				t.Fatalf("MCPDetail = %q; want %q", got, tt.want)
			}
		})
	}
}
