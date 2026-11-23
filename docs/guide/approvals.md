# Approvals and guards

Answer Codex approvals and guard remote writes and sudo commands.

## Approvals

Codex approvals open over an empty composer; otherwise press `Ctrl-B`, then `q`. Arrows
choose, Enter confirms, Esc hides without answering. Typing/pasting in Mekugi-mode
command approvals selects denial with an optional reason; feedback reaches the current
turn. Your draft/attachments return afterward. Amber choices last beyond this
request: for the turn, the session, or as a saved policy.

At interactive startup or session replacement, Mekugi asks about untrusted hooks
and hooks whose hash has changed, before accepting work. Review the command, source
and event in the approval dock. **Continue without trusting**, the initial terminal
choice, leaves trust unchanged. **Trust this hook** persists Codex's current
hash, so unchanged hooks need no further approval on later launches. A failed
save leaves the prompt open for retry or decline. Trust does not enable disabled
hooks. Headless runs and prepared child runtimes do not grant trust automatically.

The shell guard asks before supported Git, GitHub CLI, Mercurial, SVN, and
Jujutsu remote writes in interactive sessions, including with `--yolo`.
Headless runs do not prompt. Your `PreToolUse` hooks still run first.
Approve once, approve the exact command, executable, and working directory for
this UI attachment, or deny. Denial, a five-minute timeout, or an unreachable
approval channel fails that command with status 1. Subsequent commands follow
normal shell control flow. Session grants expire when the attachment closes.

The guard is not a sandbox. Commands that construct executable paths at
runtime or invoke tools outside the guarded search path may not be recognized.

Interactive sudo commands ask for approval even with cached credentials or
passwordless sudo. If authentication is needed, a separate secret dialog keeps
the password out of model input and retained history. You can approve the
command in the WebUI, but must enter passwords and passphrases in the terminal.
Explicitly noninteractive sudo (`-n` or `--non-interactive`) does not prompt or retry; sudo returns its
own output and exit status.

To disable remote-write approvals, sudo approvals, and GPG passphrase entry,
set `shell_guard = false` under `[features]` in Codex's `config.toml` or pass
`-c features.shell_guard=false` to Codex. Neither setting grants sandbox
permissions.

GPG signing and decryption, including Git signing, request passphrases through the
same secret editor. GPG keeps ownership of authentication and cached keys; a wrong
answer fails the command without retrying or falling back to an unsigned result.
Skipping denies authentication. Cancellation discards the pending secret draft.
No global GPG configuration is changed. Explicit caller-owned authentication/input
modes stay native; non-passphrase inquiries require caller-owned `--command-fd`.
