package claude

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

const inputLimit = 256 << 10

type toolBlock struct {
	parent, message string
	index           int
}
type toolInput struct {
	id, name, input string
	overflow        bool
}

func (a *adapter) startTool(parent string, index int, id, name string) {
	if a.tools == nil {
		a.tools = make(map[toolBlock]*toolInput)
	}
	if len(a.tools) >= 128 {
		return
	}
	a.tools[toolBlock{parent, a.streams[parent], index}] = &toolInput{id: id, name: name}
}

func (a *adapter) toolDelta(parent string, index int, delta string, final bool) []session.Event {
	key := toolBlock{parent, a.streams[parent], index}
	t := a.tools[key]
	if final {
		delete(a.tools, key)
	}
	if t == nil || t.overflow {
		return nil
	}
	if len(t.input)+len(delta) > inputLimit {
		t.input, t.overflow = "", true
		return []session.Event{{Kind: "notice", Text: "Tool input exceeds the live preview limit; native execution is unchanged"}}
	}
	t.input += delta
	if edit := decodeEdit(t.name, t.input, final); edit != nil {
		return []session.Event{{Kind: "edit", ID: t.id, Role: t.name, Caller: parent, Edit: edit}}
	}
	return nil
}

type stringField struct {
	text     string
	complete bool
}

// Decode only complete path/operand fields and a safely decoded content prefix.
// jsontext validates structure and duplicate fields; an unfinished escape stays
// buffered until the next delta, including paired UTF-16 surrogate escapes.
func editFields(input string) map[string]stringField {
	d := jsontext.NewDecoder(strings.NewReader(input))
	t, err := d.ReadToken()
	if err != nil || t.Kind() != '{' {
		return nil
	}
	fields := make(map[string]stringField)
	for {
		t, err = d.ReadToken()
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return fields
			}
			return nil
		}
		if t.Kind() == '}' {
			if _, err := d.ReadToken(); !errors.Is(err, io.EOF) {
				return nil
			}
			return fields
		}
		if t.Kind() != '"' {
			return nil
		}
		name, offset := t.String(), d.InputOffset()
		v, err := d.ReadValue()
		if err != nil {
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			raw := strings.TrimLeft(input[offset:], ": \t\r\n")
			if strings.HasPrefix(raw, "\"") {
				for drop := 0; drop <= 12 && drop < len(raw); drop++ {
					var value string
					if json.Unmarshal([]byte(raw[:len(raw)-drop]+"\""), &value) == nil {
						fields[name] = stringField{text: value}
						break
					}
				}
			}
			return fields
		}
		var value string
		if v.Kind() == '"' && json.Unmarshal(v, &value) == nil {
			fields[name] = stringField{value, true}
		}
	}
}

func decodeEdit(tool, input string, final bool) *session.Edit {
	if tool != "Write" && tool != "Edit" || len(input) > inputLimit {
		return nil
	}
	if final && !jsontext.Value(input).IsValid() {
		return nil
	}
	f := editFields(input)
	path := f["file_path"]
	if !path.complete || path.text == "" {
		return nil
	}
	key := "content"
	if tool == "Edit" {
		key = "new_string"
	}
	content, ok := f[key]
	if !ok {
		return nil
	}
	edit := &session.Edit{Path: path.text, Content: content.text, Partial: !final, Replace: tool == "Edit"}
	if edit.Replace {
		old := f["old_string"]
		if !old.complete || old.text == "" {
			return nil
		}
		edit.Old = old.text
		if final {
			var options struct {
				ReplaceAll bool `json:"replace_all"`
			}
			if json.Unmarshal([]byte(input), &options) != nil {
				return nil
			}
			edit.ReplaceAll = options.ReplaceAll
		}
	}
	return edit
}
