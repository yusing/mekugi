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
