package responses

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"reflect"
)

// Source: internal/router/request_wire.go:84:110@06fc40e4 sameJSONValue.
var preserveNumbers = json.WithUnmarshalers(json.UnmarshalFromFunc(func(decoder *jsontext.Decoder, value *any) error {
	if decoder.PeekKind() != '0' {
		return errors.ErrUnsupported
	}
	raw, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	*value = jsonv1.Number(raw)
	return nil
}))

// SameJSONValue compares decoded JSON without losing numeric spelling or precision.
// It retains the wire's permissive duplicate-name and invalid-UTF-8 semantics.
func SameJSONValue(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	var a, b any
	if json.Unmarshal(left, &a, jsonv1.DefaultOptionsV1(), preserveNumbers) != nil {
		return false
	}
	return json.Unmarshal(right, &b, jsonv1.DefaultOptionsV1(), preserveNumbers) == nil && reflect.DeepEqual(a, b)
}
