Pass `shell:"bash"` to `tools.exec_command` unless the task explicitly requires another shell; Codex may otherwise select Zsh even when the login shell is Fish.
