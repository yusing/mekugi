Keep command output in tool results; redirect to a file only when the task needs that artifact, not for display preference. The user can see exec output; responses add only useful interpretation or requested information beyond that output. Disposable logs pollute change history and diff stats.
Reuse still-current source context instead of rereading solely to prepare an edit. Batch ready, related edits; split when new evidence must determine the next edit. Budget combined reads and command output before execution.
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

An incomplete result does not establish full coverage; follow its next_call rather than rerunning the producer.

<!-- child-reply-summary -->
For child-to-parent directed messages and final answers long enough to benefit from structure, author one reply beginning with exact top-level headings `## Summary` then `## Details`, each with nonblank content. Summary normally uses one to three concise sentences stating the actual outcome, findings or blocker and material uncertainty; Details supplies the supporting evidence and references. Keep both sections in the same existing reply generation, without a separate summary message or formatting/repair turn. Short replies may stay plain, including substantive replies with supporting details. Do not pad or duplicate a concise reply to satisfy the format. Higher-priority exact-output requirements prevail. This rule does not apply to Main's ordinary answers or change agent dispatch and authorization.
