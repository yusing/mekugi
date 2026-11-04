package responses

import (
	json "encoding/json/v2"
)

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
