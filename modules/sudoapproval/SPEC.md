# Sudo approval

## Requirements

- Guard interactive `sudo` invocations with UI approval (even with cached credentials or
  passwordless `sudo`, independently of `--yolo`), with an explicit opt-out.
- Collect `sudo` passwords and GPG passphrases (including Git signing) through a dedicated private
  secret dialog so secrets never enter model input, command arguments, environment variables, input
  history, or retained replay/session logs.

## Current approach (replaceable)

- Shares the synchronous native `PreToolUse` command hook, `mekugi-exec` wrapper instrumentation,
  session-scoped exact-command grants, and script/wrapper coverage limits of
  [Remote-write approval](../remotewriteapproval/SPEC.md) (headless runs and `--shell-guard=false`
  bypass it). `sudo -l`/help/version/timestamp-reset and explicit noninteractive mode stay native.
- **Sudo authentication**: approval and password entry are separate; `sudo`'s askpass invokes the
  private secret dialog over the local approval socket/pipe only when `sudo` actually needs a
  password (session command grants never answer passwords).
- **GPG passphrases**: wraps supported `gpg`/`gpg2` invocations with per-command loopback inquiries
  (never editing global GPG config; respecting caller-owned pinentry/passphrase/`--command-fd`
  flags and cached `gpg-agent` keys). Wrong answers or skips fail natively without silent retry or
  unsigned fallback; non-passphrase inquiries fail with an explanation.
