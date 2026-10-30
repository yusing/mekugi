# Approvals and guards

Answer Codex approvals and guard remote writes and sudo commands.

## Approvals

Codex approvals open over an empty composer; otherwise press `Ctrl-B`, then `q`. Arrows
choose, Enter confirms, Esc hides without answering. Typing/pasting in Mekugi-mode
command approvals selects denial with an optional reason; feedback reaches the current
turn. Your draft/attachments return afterward.

The guard asks before supported Git/GitHub/Mercurial/SVN/Jujutsu remote writes in
interactive Mekugi mode, even with `--yolo`; headless/noninteractive runs have no guard.
It needs `mekugi-exec` beside `mekugi`, synchronous native command hooks, and unsandboxed execution.
Approve once, approve the exact command/workdir/executable for this UI session, or deny.
Denial, a five-minute timeout, or an unreachable approval connection fails only that
command with status 1; the shell follows normal `;`/`&&` behavior. Session grants expire
when the UI closes. At startup, incompatible caller hooks produce a warning and
are disabled for that invocation. Other hooks and configuration files remain
unchanged. Managed conflicts, unverifiable matchers, or missing/conflicting guard
hooks at turn admission block with an explanation. Read-only commands run without asking.

Coverage is incomplete for runtime-computed commands, `eval`, scripts/sourced files, and
arbitrary programs invoking VCS internally. See [guard
limits](../../modules/remotewriteapproval/SPEC.md).

Interactive sudo commands also ask for approval, even with cached credentials or
passwordless sudo. If authentication is needed afterward, a separate secret password
dialog keeps the answer out of model input and retained history. The shared
`--shell-guard` toggle controls both sudo and remote-write approval and grants no
sandbox permissions. See
[sudo support and limits](../../modules/sudoapproval/SPEC.md).
