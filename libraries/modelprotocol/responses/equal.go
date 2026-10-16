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
	a, err := DecodeJSONValue(left)
	if err != nil {
		return false
	}
	b, err := DecodeJSONValue(right)
	return err == nil && reflect.DeepEqual(a, b)
}

// DecodeJSONValue retains exact number spelling and permissive wire semantics.
func DecodeJSONValue(raw []byte) (any, error) {
	var value any
	err := json.Unmarshal(raw, &value, jsonv1.DefaultOptionsV1(), preserveNumbers)
	return value, err
}
