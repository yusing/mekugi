package codexbinding

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

// Source: apps/mekugi/internal/router/native_apply_patch.go:117:196,294:300@72549ff1.
// Decode arriving patch text without requiring the closing quote, call, or
// envelope. This is display-only and must never feed capture or execution.
func stockPatchFragment(source string) string {
	if len(source) > commandanalysis.MaxScriptBytes {
		return ""
	}
	ranges, _, _ := codeModePreviewSyntax(source)
	rangeIndex := 0
	patch := ""
	for at := 0; at < len(source); {
		if end := codeModeRangeEnd(ranges, &rangeIndex, at); end > at {
			at = end
			continue
		}
		if next := codeModeSkipComment(source, at); next > at {
			at = next
			continue
		}
		if source[at] == '\'' || source[at] == '"' || source[at] == '`' {
			value, consumed := toolActivityJavaScriptStringFragment(source[at:])
			if _, ok := nativePatchPreview(value); ok {
				patch = value
			}
			at += max(1, consumed)
			continue
		}
		at++
	}
	return patch
}

func toolActivityJavaScriptStringFragment(source string) (string, int) {
	quote := source[0]
	var value strings.Builder
	for offset := 1; offset < len(source); {
		if quote == '`' && strings.HasPrefix(source[offset:], "${") {
			return value.String(), codeModeSkipString(source, 0)
		}
		if source[offset] == quote {
			return value.String(), offset + 1
		}
		if quote == '`' && (source[offset] == '\r' || source[offset] == '\n') {
			value.WriteByte(source[offset])
			offset++
			continue
		}
		if source[offset] == '\r' || source[offset] == '\n' {
			return value.String(), offset
		}
		if source[offset] == '\\' && offset+1 < len(source) && (strings.ContainsRune(`'"/\\`, rune(source[offset+1])) || quote == '`' && (source[offset+1] == '`' || source[offset+1] == '$')) {
			value.WriteByte(source[offset+1])
			offset += 2
			continue
		}
		char, _, tail, err := strconv.UnquoteChar(source[offset:], quote)
		if err != nil {
			// Go and JavaScript accept different escape sets. If this display-only
			// decoder cannot interpret one, recover at the string's lexical end so
			// a later patch literal is still inspected.
			for cursor := offset; cursor < len(source); {
				if source[cursor] == '\\' {
					cursor = min(len(source), cursor+2)
					continue
				}
				if source[cursor] == quote {
					return value.String(), cursor + 1
				}
				if source[cursor] == '\r' || source[cursor] == '\n' {
					return value.String(), cursor
				}
				_, size := utf8.DecodeRuneInString(source[cursor:])
				cursor += max(1, size)
			}
			return value.String(), len(source)
		}
		value.WriteRune(char)
		offset = len(source) - len(tail)
	}
	return value.String(), len(source)
}

func nativePatchPreview(input string) (previewmodel.Preview, bool) {
	// Incomplete input is useful provisional display, never application evidence.
	if input != "*** Begin Patch" && !strings.HasPrefix(input, "*** Begin Patch\n") && !strings.HasPrefix(input, "*** Begin Patch\r\n") {
		return previewmodel.Preview{}, false
	}
	return previewmodel.Preview{Input: input, DiffText: true, Status: previewmodel.PreviewEdit}, true
}
