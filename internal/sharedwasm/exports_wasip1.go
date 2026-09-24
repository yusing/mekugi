package main

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"
	"unsafe"

	"github.com/yusing/mekugi/internal/golex"
	"github.com/yusing/mekugi/internal/gooutline"
	"github.com/yusing/mekugi/internal/logicalrow"
	"github.com/yusing/mekugi/internal/quotedoperand"
	"github.com/yusing/mekugi/internal/shellsyntax"
	"github.com/yusing/mekugi/internal/sourcekind"
)

const abiVersion = 1

const (
	operationParsePositiveInteger = iota + 2
	operationDecodeQuotedOperand
	operationClassifySourcePath
	operationIsGoIdentifier
	operationDecodeGoStringLiteral
	operationParseShellHeader
	operationInterpreterIdentity
	operationGoOutline
)

const maxJavaScriptSafeInteger = 1<<53 - 1

var (
	inputBuffer  []byte
	resultBuffer []byte
	boundsBuffer [3]uint32
)

// coreError is the structured error result for shared-core operations.
type coreError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// coreResponse is the JSON response envelope for shared-core invocations.
type coreResponse struct {
	OK    bool       `json:"ok"`
	Value any        `json:"value,omitempty"`
	Error *coreError `json:"error,omitempty"`
}

// exportedABIVersion returns the shared-core WASM ABI version for compatibility checking.
//
//go:wasmexport mekugi_core_abi_version
func exportedABIVersion() uint32 {
	return abiVersion
}

// reserveInput reserves an input buffer of the given size and returns its WASM pointer.
//
//go:wasmexport mekugi_core_reserve_input
func reserveInput(size uint32) uint32 {
	if cap(inputBuffer) < int(size) {
		inputBuffer = make([]byte, size)
	} else {
		inputBuffer = inputBuffer[:size]
	}
	if len(inputBuffer) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(inputBuffer))))
}

// lineCount returns the number of targetable logical lines in the input buffer.
//
//go:wasmexport mekugi_core_line_count
func lineCount() uint32 {
	return uint32(logicalrow.Count(string(inputBuffer)))
}

// lineBounds returns a WASM pointer to a three-element array containing the start, content-end, and full-end offsets for the given line number.
//
//go:wasmexport mekugi_core_line_bounds
func lineBounds(lineNumber uint32) uint32 {
	line, ok := logicalrow.At(string(inputBuffer), int(lineNumber))
	if !ok {
		return 0
	}
	boundsBuffer = [3]uint32{uint32(line.Start), uint32(line.ContentEnd), uint32(line.End)}
	return uint32(uintptr(unsafe.Pointer(&boundsBuffer[0])))
}

// invoke executes one shared-core operation on the input buffer and returns the JSON result byte length.
//
//go:wasmexport mekugi_core_invoke
func invoke(operation uint32) uint32 {
	if !utf8.Valid(inputBuffer) {
		return encodeFailure("invalid_utf8", "shared-core input is not UTF-8")
	}
	input := string(inputBuffer)
	var value any
	var err *coreError
	switch operation {
	case operationParsePositiveInteger:
		value, err = parsePositiveInteger(input)
	case operationDecodeQuotedOperand:
		decoded, rest, decodeErr := quotedoperand.DecodeQuoted(input)
		if decodeErr != nil {
			err = &coreError{Code: "invalid_quoted_operand", Message: decodeErr.Error()}
		} else {
			value = map[string]string{"value": decoded, "rest": rest}
		}
	case operationClassifySourcePath:
		format, ok := sourcekind.Classify(input)
		if ok {
			value = format
		}
	case operationIsGoIdentifier:
		value = golex.IsIdentifier(input)
	case operationDecodeGoStringLiteral:
		decoded, decodeErr := golex.DecodeStringLiteral(input)
		if decodeErr != nil {
			err = &coreError{Code: "invalid_go_string_literal", Message: decodeErr.Error()}
		} else {
			value = decoded
		}
	case operationParseShellHeader:
		parsed, parseErr := shellsyntax.Parse(input)
		if parseErr != nil {
			err = &coreError{Code: "invalid_shell_header", Message: parseErr.Error()}
		} else {
			value = parsed
		}
	case operationInterpreterIdentity:
		value = shellsyntax.InterpreterIdentity(input)
	case operationGoOutline:
		value = gooutline.Parse(input)
	default:
		err = &coreError{Code: "unknown_operation", Message: "shared-core operation is unavailable"}
	}
	if err != nil {
		return encode(coreResponse{Error: err})
	}
	return encode(coreResponse{OK: true, Value: value})
}

// resultPointer returns the WASM pointer to the JSON result buffer from the last invoke call.
//
//go:wasmexport mekugi_core_result_pointer
func resultPointer() uint32 {
	if len(resultBuffer) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(resultBuffer))))
}

// parsePositiveInteger parses a positive decimal integer within JavaScript's safe integer range.
func parsePositiveInteger(input string) (any, *coreError) {
	if input == "" || input[0] == '0' {
		return nil, &coreError{Code: "invalid_positive_integer", Message: "value must be a positive decimal integer"}
	}
	for _, character := range input {
		if character < '0' || character > '9' {
			return nil, &coreError{Code: "invalid_positive_integer", Message: "value must be a positive decimal integer"}
		}
	}
	value, err := strconv.ParseUint(input, 10, 53)
	if err != nil || value > maxJavaScriptSafeInteger {
		return nil, &coreError{Code: "integer_out_of_range", Message: "value is too large"}
	}
	return value, nil
}

// encodeFailure encodes an error response and returns its byte length.
func encodeFailure(code, message string) uint32 {
	return encode(coreResponse{Error: &coreError{Code: code, Message: message}})
}

// encode marshals a response to JSON and returns its byte length.
func encode(response coreResponse) uint32 {
	encoded, err := json.Marshal(response)
	if err != nil {
		encoded = []byte(`{"ok":false,"error":{"code":"encoding_failure","message":"shared-core result could not be encoded"}}`)
	}
	resultBuffer = encoded
	return uint32(len(resultBuffer))
}
