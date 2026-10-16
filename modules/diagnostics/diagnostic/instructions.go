// Source: internal/router/debug_instructions.go@[ac8511fa] compact instruction records.
package diagnostic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
)

const instructionSchema = "mekugi.instructions.v2"
const instructionDictionaryLimit = 4096

var instructionContentKeys = []string{"instructions", "developer_messages", "tools", "additional_tools", "wire_developer_messages", "wire_additional_tools"}

func instructionDigest(value jsontext.Value) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// References stay within this append-only file. The writer retains only digests,
// not private payloads, and falls back to inline values when its dictionary fills.
func (d *Bundle) Instructions(fields map[string]any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return
	}
	if d.instructionContent == nil {
		d.instructionContent = make(map[string]bool)
	}
	refs := make(map[string]string)
	for _, key := range instructionContentKeys {
		value, err := json.Marshal(fields[key], json.Deterministic(true))
		if err != nil {
			d.err = err
			return
		}
		fields[key] = jsontext.Value(value)
		if len(value) <= 128 {
			continue
		}
		digest := instructionDigest(value)
		if d.instructionContent[digest] {
			refs[key] = digest
			delete(fields, key)
		} else if len(d.instructionContent) < instructionDictionaryLimit {
			d.instructionContent[digest] = true
		}
	}
	fields["schema"] = instructionSchema
	if len(refs) != 0 {
		fields["content_refs"] = refs
	}
	d.err = json.MarshalEncode(jsontext.NewEncoder(d.writes.Writer(d.dump)), &fields, json.Deterministic(true))
}

// The inspector also accepts legacy inline records. Resolve before selecting a
// request so shared content remains available without exposing unrelated text.
type instructionDecoder struct {
	content map[string]jsontext.Value
}

func (d *instructionDecoder) resolve(row map[string]jsontext.Value) error {
	if _, compact := row["content_refs"]; !compact && recordString(row, "schema") != instructionSchema {
		return nil
	}
	if recordString(row, "schema") != instructionSchema {
		return errors.New("unsupported instruction reference schema")
	}
	var refs map[string]string
	if raw, ok := row["content_refs"]; ok {
		if err := json.Unmarshal(raw, &refs); err != nil {
			return fmt.Errorf("instruction references: %w", err)
		}
	}
	if d.content == nil {
		d.content = make(map[string]jsontext.Value)
	}
	for _, key := range instructionContentKeys {
		value, ok := row[key]
		if !ok || len(value) <= 128 {
			continue
		}
		if len(d.content) < instructionDictionaryLimit {
			d.content[instructionDigest(value)] = value
		}
	}
	for key, digest := range refs {
		if !slices.Contains(instructionContentKeys, key) {
			return fmt.Errorf("unsupported instruction reference field %q", key)
		}
		if _, inline := row[key]; inline {
			return fmt.Errorf("instruction field %s is both inline and referenced", key)
		}
		value, ok := d.content[digest]
		if !ok {
			return fmt.Errorf("instruction content unavailable for %s", key)
		}
		row[key] = value
	}
	return nil
}
