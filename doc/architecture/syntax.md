# Shared compact-script framing

## CTR-SYNTAX-001 — Shared compact-script framing

One shared component owns quoted operands, heredoc framing, decoded command frames,
and comparable target identity for the compact edit syntax in `REQ-SCRIPT-001`. The
engine and rejected-script recovery share that framing but retain separate ownership of
filesystem evaluation, ancestry, correction policy, and output.

Root engine parsing remains edit-only. The shell interpreter owns argument expansion,
redirection, and the outer heredoc. The worker passes the resulting argument or stdin
bytes to the edit parser without another shell interpretation.
