package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJournalToolErrorsNameTreeOperations(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	for arguments, want := range map[string]string{
		`{"op":"read","title":"Wrong"}`: "journal read accepts only p, agent, depth",
		`{"op":"list","p":"/1"}`:        "journal list accepts only agent",
	} {
		result, err := transform.executeJournalCall(map[string]json.RawMessage{
			"type": mustMarshalJSON("function_call"), "name": mustMarshalJSON("journal"),
			"call_id": mustMarshalJSON(arguments), "arguments": mustMarshalJSON(arguments),
		})
		if err != nil {
			t.Fatal(err)
		}
		if output := jsonString(result, "output"); !strings.Contains(output, want) {
			t.Fatalf("%s: error %s, want %q", arguments, output, want)
		}
	}
}
