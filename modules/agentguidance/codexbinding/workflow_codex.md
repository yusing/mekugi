The user can see exec output; responses add only useful interpretation or requested information beyond that output.
Print each exec_command result's output once, with its status/session ID; do not serialize whole result envelopes or arrays. For independent commands, batch them and print labeled outputs:
```js
const results = await Promise.allSettled([
  tools.exec_command({cmd: "rg -n Main src/main.go", max_output_tokens: 2000}),
  tools.exec_command({cmd: "rg -n TestThing .", max_output_tokens: 2000}),
]);
const labels = ["source", "tests"];
for (let i = 0; i < results.length; i++) {
  const result = results[i];
  if (result.status === "rejected") { text(`${labels[i]}: ${result.reason}`); continue; }
  const value = result.value;
  text(`${labels[i]}: ${value.session_id ? `running session_id=${value.session_id}` : `exit_code=${value.exit_code}`}\n${value.output}`);
}
```
Bound unknown-volume producers before execution: select needed JSON fields or use mrun. Sum every command's output budget, leaving room for labels and recovery handles within the outer exec limit, 10000 tokens by default. Raise it before the calls with a first-line pragma such as `// @exec: {"max_output_tokens": 16000}`, or request less output. Inner limits cannot raise the outer limit; overflow can erase next_call handles.

Codex compaction resets context without generating a summary. For repeated resets, you may keep reusable task state in a temporary JSON artifact outside the repository. Keep only the current goal, constraints, blockers, next step and needed facts; update changed facts and remove obsolete history. Select only needed sources with compact.reload or read them on demand after reset. Never automatically reload the whole artifact. Omit reload when no known sources are needed. Keep the artifact until its work is complete.
