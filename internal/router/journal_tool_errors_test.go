package router

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestJournalReadCarrierErrorsNameAcceptedFields(t *testing.T) {
	for _, op := range []string{"read", "list"} {
		arguments := []string{commentaryOnceArgument, "unused", "unused", url.PathEscape(`{"op":"` + op + `","path":"/1"}`)}
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, arguments)
		want := "journal " + op + " accepts only op and agent"
		if op == "read" {
			want = "journal read accepts only op, p, agent, depth, and view"
		}
		if !matched || err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), `"path"`) || output.Len() != 0 {
			t.Fatalf("%s invalid field: matched=%v error=%v output=%q", op, matched, err, output.String())
		}
	}
	// Invalid read fields must not fall through to the mutation publisher,
	// including when the malformed member precedes the operation selector.
	for _, arguments := range []string{`{"op":"read","depth":"one"}`, `{"depth":"one","op":"read"}`} {
		var output bytes.Buffer
		matched, err := publishCommentaryOnce(t.Context(), &output, []string{commentaryOnceArgument, "unused", "unused", url.PathEscape(arguments)})
		if !matched || err == nil || !strings.Contains(err.Error(), "journal read accepts only op, p, agent, depth, and view") || !strings.Contains(err.Error(), "/depth") || output.Len() != 0 {
			t.Fatalf("invalid depth: matched=%v error=%v output=%q", matched, err, output.String())
		}
	}
}

func TestJournalToolErrorsNameTreeOperations(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	for arguments, want := range map[string]string{
		`{"op":"read","title":"Wrong"}`:                      "journal read accepts only p, agent, depth, view",
		`{"op":"list","view":"tasks"}`:                       "journal list accepts only agent",
		`{"op":"add","title":"Wrong","agent":"/root/child"}`: "agent must name a direct child on a task",
		`{"op":"list","p":"/1"}`:                             "journal list accepts only agent",
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
