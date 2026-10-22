package responses

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCompactReadProgramInternalContinuation(t *testing.T) {
	const harness = `
import assert from "node:assert/strict";
import fs from "node:fs";
const program = fs.readFileSync(0,"utf8");
const run = new (Object.getPrototypeOf(async function(){}).constructor)("tools", "text", program);
for (const denied of [false, true]) {
 const output = [];
 let waits = 0;
 const tools = {
  async exec_command(options) {
   assert.deepEqual(options, {cmd:"mreload selected", shell:"bash", login:false});
   if (denied) throw Error("denied");
   return {output:"", session_id:7};
  },
  async write_stdin(options) {
   assert.equal(options.session_id, 7);
   assert.equal(output.length, 0, "partial acquisition must stay internal");
   return ++waits === 1 ? {output:"", session_id:7} : {output:"Reload snapshot: mread amber", exit_code:0};
  }
 };
 await run(tools, value => output.push(value));
 assert.deepEqual(output, [denied ? "CONTENT NOT ATTACHED: mreload selected: Error: denied" : "Reload snapshot: mread amber"]);
 assert.equal(waits, denied ? 0 : 2);
}
`
	cmd := exec.CommandContext(t.Context(), "node", "--input-type=module", "-e", harness)
	cmd.Stdin = strings.NewReader(CompactReadProgram("mreload selected"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reload program: %v\n%s", err, output)
	}
}
