# Rejected-script recovery

## REQ-CORRECT-001 — Rejected-script recovery

`hpatch --recover HANDLE [SCRIPT]` runs through the shell. Omitting SCRIPT reads
corrections from stdin. The reported recovery handle identifies one immutable
rejected edit in the execution directory, not a mutable "latest" rejection.
Recovery uses the existing edit engine; it is not a separate model-facing tool.

Each rejected command has a short word handle. A private fingerprint binds the
complete baseline and ordered command mapping. Recovery rejects missing, expired,
cross-directory, or mismatched records. Durable records allow the same explicit
baseline to be used after fork, agent switching, model switching, or router restart.
Concurrent branches cannot change the baseline identified by an existing handle.

Corrections have two forms, which cannot be mixed:

- Command corrections: `HANDLE TARGET`, `HANDLE target TARGET`, or
  `HANDLE value VALUE`. Each command handle appears at most once. A correction
  preserves the command's operation and every unrelated field.
- Script-text corrections: ordinary target-bearing `type` and `add` mutations
  against the retained script, not workspace files. File commands and targetless
  initializers are not accepted.

Targets and values follow the edit grammar. The complete rebuilt script must be
different, nonempty, and within the 1 MiB bound, including intermediate plans.
`EditTextBounded` owns script-text rebuilding; the ordinary edit engine owns
evaluation, formatting, and application.

An invalid correction changes neither the workspace nor the retained baseline.
A rebuilt script that fails evaluation returns a new recovery handle and fresh
command handles. Earlier immutable baselines remain unchanged. Successful recovery
keeps the original change ID, so `hchanges --history` can display the attempts.
Application failures are not evaluation rejections and do not offer automatic
recovery.

Diagnostics distinguish script rows from workspace rows. Wholly stale-row failures
list their target-bearing command handles. Other evaluation failures include bounded
script-row previews and parsed command handles. Generated-source coordinates remain
diagnostics, not recovery targets.

Acceptance:

1. Shell arguments, stdin, quoting, and redirection retain ordinary semantics.
2. Command and script-text correction forms preserve all untargeted bytes.
3. Invalid, unchanged, oversized, or conflicting corrections do not evaluate edits.
4. Rebuilt edits validate atomically before applying through the same engine.
5. Explicit immutable baselines remain isolated across concurrent requests and branches.
6. A durable recovery handle works after router restart without a live parent.
7. A missing, successful, mismatched, or cross-directory baseline rejects.
8. Native shell failures never become edit-recovery baselines.
9. Replay restores the original shell call without repeating execution.
