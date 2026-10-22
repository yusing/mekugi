package responses

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"slices"
	"strings"

	protocol "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// PrepareCompactTurn sends the selected summary prompt as an ordinary provider
// turn. Response collection and reload metadata belong to the application.
func PrepareCompactTurn(request *Request, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return errors.New("compaction requires a summary prompt")
	}
	var items []jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return err
	}
	items = slices.DeleteFunc(items, func(raw jsontext.Value) bool {
		var item struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &item)
		return item.Type == "compaction_trigger" || item.Type == "additional_tools"
	})
	items = append(items, protocol.MustMarshalJSON(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": prompt}}}))
	request.Fields["input"] = protocol.MustMarshalJSON(items)
	request.Fields["tools"] = jsontext.Value(`[]`)
	request.Fields["parallel_tool_calls"] = jsontext.Value(`false`)
	return nil
}

func CompactReadProgram(command string) string {
	data, _ := json.Marshal(command)
	return `const cmd = ` + string(data) + `;` + compactReadBody
}

// MatchesCompactReadProgram validates the exact current reader program.
func MatchesCompactReadProgram(program, command string) bool {
	return program == CompactReadProgram(command)
}

const compactReadBody = `
  try {
    let result = await tools.exec_command({cmd, shell:"bash", login:false});
    let output = result.output;
    while (result.session_id) {
      result = await tools.write_stdin({session_id:result.session_id, chars:"", yield_time_ms:300000});
      output += result.output;
    }
    text(output);
  } catch (error) { text("CONTENT NOT ATTACHED: " + cmd + ": " + String(error)); }
`
