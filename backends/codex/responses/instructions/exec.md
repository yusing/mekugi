Nested tool return values are not automatically displayed. Print command output with `text(...)`, including its exit status or running session ID:
```js
const result = await tools.exec_command({cmd: "ls"});
text(`${result.session_id ? `running session_id=${result.session_id}` : `exit_code=${result.exit_code}`}\n${result.output}`);
```
The input is JavaScript, not shell: run shell commands through `tools.exec_command`.
