# Approvals and guards

Answer Codex approvals and guard remote writes and sudo commands.

## Approvals

Codex approvals open over an empty composer; otherwise press `Ctrl-B`, then `q`. Arrows
choose, Enter confirms, Esc hides without answering. Typing/pasting in Mekugi-mode
command approvals selects denial with an optional reason; feedback reaches the current
turn. Your draft/attachments return afterward.

At interactive startup or session replacement, Mekugi asks about untrusted hooks
and hooks whose hash has changed, before accepting work. Review the command, source
and event in the approval dock. **Continue without trusting**, the initial terminal
choice, leaves trust unchanged. **Trust this hook** persists Codex's current
hash, so unchanged hooks need no further approval on later launches. A failed
save leaves the prompt open for retry or decline. Trust does not enable disabled
hooks. Headless runs and prepared child runtimes do not grant trust automatically.

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
`--shell-guard` toggle controls sudo, GPG passphrase entry and remote-write approval and grants no
sandbox permissions. See
[sudo support and limits](../../modules/sudoapproval/SPEC.md).

GPG signing and decryption, including Git signing, request passphrases through the
same secret editor. GPG keeps ownership of authentication and cached keys; a wrong
answer fails the command without retrying or falling back to an unsigned result.
Skipping denies authentication. Cancellation discards the pending secret draft.
No global GPG configuration is changed. Explicit caller-owned authentication/input
modes stay native; non-passphrase inquiries require caller-owned `--command-fd`.
See [GPG support and limits](../../modules/sudoapproval/SPEC.md#gpg-passphrases).
