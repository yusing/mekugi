package binding

import (
	"encoding/json/jsontext"
	"testing"
)

func TestDecodeCommandObservationIdentityAndOptionalOutcome(t *testing.T) {
	value, ok := DecodeCommandObservation("item/completed", jsontext.Value(`{"threadId":"thread","turnId":"turn","itemId":"outer","item":{"id":"nested","type":"commandExecution","command":"bash -c 'true'","aggregatedOutput":"","exitCode":0}}`))
	if !ok || value.Key != [3]string{"thread", "turn", "outer"} || value.Command != "bash -c 'true'" || value.Output == nil || *value.Output != "" || value.Exit == nil || *value.Exit != 0 {
		t.Fatalf("identity precedence or supplied empty/zero outcome lost: %+v, %v", value, ok)
	}
	value, ok = DecodeCommandObservation("item/started", jsontext.Value(`{"threadId":"thread","turnId":"turn","item":{"id":"nested","type":"commandExecution","command":"sh -c 'true'"}}`))
	if !ok || value.Key != [3]string{"thread", "turn", "nested"} || value.Output != nil || value.Exit != nil {
		t.Fatalf("absent aggregate or unknown exit invented a value: %+v, %v", value, ok)
	}
	value, ok = DecodeCommandObservation("item/commandExecution/outputDelta", jsontext.Value(`{"threadId":"thread","turnId":"turn","itemId":"outer","delta":"exact\n"}`))
	if !ok || value.Key != [3]string{"thread", "turn", "outer"} || value.Delta != "exact\n" || value.Output != nil || value.Exit != nil {
		t.Fatalf("output delta changed identity or payload: %+v, %v", value, ok)
	}
}

func TestDecodeCommandObservationRejectsUnrelatedOrMalformedEvents(t *testing.T) {
	for _, test := range []struct{ method, raw string }{
		{"item/started", `{"item":{"type":"fileChange"}}`},
		{"item/completed", `{"item":{"type":"commandExecution","exitCode":"0"}}`},
		{"item/commandExecution/outputDelta", `{`},
		{"item/other", `{"item":{"type":"commandExecution"}}`},
	} {
		if _, ok := DecodeCommandObservation(test.method, jsontext.Value(test.raw)); ok {
			t.Errorf("accepted %s: %s", test.method, test.raw)
		}
	}
}
