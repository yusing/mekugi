The user can see exec output; responses add only useful interpretation or requested information beyond that output.
For parallel exec commands, print labeled outputs without serializing result envelopes:
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
Budget the combined outputs within the outer exec limit, 10000 tokens by default. Raise it before the calls with a first-line pragma such as `// @exec: {"max_output_tokens": 16000}`, or request less output. Inner `max_output_tokens` and `--max-tokens` limits cannot raise it; overflow can erase next_call handles.
