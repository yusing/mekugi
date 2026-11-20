# Remote-write approval

## Requirements

- Guard supported remote version-control writes (Git, GitHub CLI, Mercurial, Subversion, Jujutsu)
  in interactive sessions with a UI approval prompt, even when running with `--yolo`, without
  replacing Codex execution or modifying user configuration.
- Denial, timeout, or an unreachable approval channel must fail closed for that command without
  running the remote write.
- Provide an explicit opt-out from shell guarding while retaining Codex policy and independent
  command tracking.

## Current approach (replaceable)

- Uses Codex's synchronous native `PreToolUse` command hook and `mekugi-exec` to instrument shell
  commands and wrappers (`env`, `command`, `exec`, `nohup`, `timeout`, `nice`, `xargs`, nested
  `-c` shells) so approval prompts fire only when execution reaches a classified remote write.
  Requires interactive, unsandboxed execution (`danger-full-access` or `--yolo`); headless runs
  have no guard.
- Offers approve-once, session grant for the exact expanded argv/workdir/executable (scoped to the
  answering UI attachment, cleared on detach/resume), or deny (optional reason printed to stderr,
  exiting 1 so `;`/`&&`/`||` continue normally; 5-minute timeout).
- Startup validates effective workspace hooks, warns/disables incompatible user hooks for the
  invocation only, and blocks guarded turns if conflicting/untrusted hooks appear later.
- **Known limits**: protects against accidental remote writes, not arbitrary process execution;
  cannot exhaustively cover fully computed executables without a tool-name suffix, `eval`,
  sourced/external scripts, or programs invoking VCS binaries directly.
