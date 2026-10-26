package codexbinding

import (
	"bytes"
	"testing"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestProjectMessageIDs(t *testing.T) {
	for _, kind := range []string{"message", "item_reference", "function_call"} {
		for _, role := range []string{"assistant", "user"} {
			t.Run(kind+"/"+role, func(t *testing.T) {
				item := map[string]any{"type": kind, "role": role, "id": control.MessageItemID("retained:message"), "content": "unchanged"}
				input := responses.MustMarshalJSON([]any{item})
				before := bytes.Clone(input)
				var err error
				input, err = ProjectMessageIDs(input)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "message" && role == "assistant" {
					delete(item, "id")
					if !responses.SameJSONValue(input, responses.MustMarshalJSON([]any{item})) {
						t.Fatal("message changed beyond local ID")
					}
				} else if !bytes.Equal(before, input) {
					t.Fatal("unrelated item changed")
				}
				projected := bytes.Clone(input)
				input, err = ProjectMessageIDs(input)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(projected, input) {
					t.Fatal("projection is not idempotent")
				}
			})
		}
	}
}
