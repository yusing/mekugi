package providercodec

// Source: internal/router/request_wire.go:84:94@5a2082938e797e6e15c374eb6ae135a0753dea24 marshalProtocolJSON
// Source: internal/router/mekugi_proxy.go:706:718@5a2082938e797e6e15c374eb6ae135a0753dea24 mustMarshalJSON and jsonString

import (
	"bytes"
	"encoding/json"
)

// Preserve protocol spelling, including sorted map keys and unescaped HTML.
func mustMarshalJSON(value any) json.RawMessage {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'})
}

func jsonString(object map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(object[name], &value)
	return value
}
