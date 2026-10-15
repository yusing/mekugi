package record

import (
	"crypto/sha256"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const MaxOutputBytes = 16 << 20

// Retained records and their integrity hashes use the complete historical v1
// canonical encoding, including field order, escaping, and nil collections.
var LegacyJSON = jsonv1.DefaultOptionsV1()

type Record struct {
	SourceRow    uint64         `json:"source_row,omitzero"`
	Changes      jsontext.Value `json:"changes,omitempty"`
	Version      int            `json:"version"`
	ID           string         `json:"id"`
	Stdout       string         `json:"stdout"`
	Stderr       string         `json:"stderr"`
	ExitCode     int            `json:"exit_code"`
	StdoutKind   string         `json:"stdout_kind,omitempty"`
	StderrKind   string         `json:"stderr_kind,omitempty"`
	Source       string         `json:"source,omitempty"`
	Position     [2]int         `json:"position,omitempty"`
	Stream       string         `json:"stream,omitempty"`
	CursorDigest string         `json:"cursor_digest,omitempty"`
	Binding      string         `json:"binding,omitempty"`
}

func ValidID(id string) bool              { _, ok := history.ParseHandle(id); return ok }
func validReadKind(kind string) bool      { return kind == "" || kind == "rows" || kind == "json" }
func Encode(value Record) ([]byte, error) { return json.Marshal(&value, LegacyJSON) }
func Binding(value Record) string {
	data, _ := Encode(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func CursorDigest(value Record) string { value.ID, value.CursorDigest = "", ""; return Binding(value) }
func Validate(record Record) error {
	if size := len(record.Stdout) + len(record.Stderr); size > MaxOutputBytes {
		return &history.CapacityError{Resource: "saved read output", Required: int64(size), Limit: MaxOutputBytes, Action: "Narrow the command output or split the operation before retrying."}
	}
	if record.Version != 1 || !ValidID(record.ID) ||
		record.ExitCode < 0 || record.ExitCode > 255 ||
		!utf8.ValidString(record.Stdout) || !utf8.ValidString(record.Stderr) ||
		!validReadKind(record.StdoutKind) || !validReadKind(record.StderrKind) {
		return errors.New("invalid read recovery record")
	}
	if record.SourceRow != 0 && (record.StdoutKind != "rows" || record.SourceRow > 1<<53-1) {
		return errors.New("invalid source row provenance")
	}
	if len(record.Changes) != 0 && (record.Source != "" || record.Stdout != "" || record.Stderr != "" || record.StdoutKind != "" || record.StderrKind != "") {
		return errors.New("invalid change read selection")
	}
	if record.Stream != "" && record.Stream != "stdout" && record.Stream != "stderr" {
		return errors.New("invalid read stream selection")
	}
	if record.Source != "" {
		if !ValidID(record.Source) || record.Source == record.ID ||
			record.Stdout != "" || record.Stderr != "" || record.StdoutKind != "" || record.StderrKind != "" ||
			record.Position[0] < 0 || record.Position[1] < 0 || len(record.Binding) != 64 || record.CursorDigest != CursorDigest(record) {
			return errors.New("invalid read continuation record")
		}
	} else if record.Position != [2]int{} || record.Stream != "" || record.Binding != "" || record.CursorDigest != "" {
		return errors.New("invalid initial read reference")
	}
	for index, text := range []string{record.Stdout, record.Stderr} {
		kind := []string{record.StdoutKind, record.StderrKind}[index]
		if kind == "rows" && text != "" && !strings.HasSuffix(text, "\n") {
			return errors.New("verified rows must have complete line endings")
		}
		if kind == "json" {
			var entries []jsontext.Value
			if json.Unmarshal([]byte(text), &entries, LegacyJSON) != nil || entries == nil {
				return errors.New("structured read output must be a JSON array")
			}
		}
	}
	return nil
}

// Decode preserves the strict recovery and permissive historical-adoption paths.
func Decode(data []byte, id string, strict bool, canonicalize func([]byte, bool) ([]byte, error)) (Record, error) {
	var value Record
	if strict {
		required := struct {
			*Record
			Stdout   *string `json:"stdout"`
			Stderr   *string `json:"stderr"`
			ExitCode *int    `json:"exit_code"`
		}{Record: &value}
		if err := json.Unmarshal(data, &required, LegacyJSON, json.RejectUnknownMembers(true)); err != nil {
			return value, err
		}
		if required.Stdout == nil || required.Stderr == nil || required.ExitCode == nil {
			return value, errors.New("read recovery record is missing required fields")
		}
		value.Stdout, value.Stderr, value.ExitCode = *required.Stdout, *required.Stderr, *required.ExitCode
	} else if err := json.Unmarshal(data, &value, LegacyJSON); err != nil {
		return value, err
	}
	if string(value.Changes) == "null" {
		value.Changes = nil
	}
	if len(value.Changes) > 0 {
		if canonicalize == nil {
			return value, errors.New("change reference resolver is unavailable")
		}
		data, err := canonicalize(value.Changes, strict)
		if err != nil {
			return value, err
		}
		value.Changes = data
	}
	if value.ID != id {
		return value, errors.New("read recovery identity mismatch")
	}
	return value, Validate(value)
}
