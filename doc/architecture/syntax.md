# Shared compact-script framing

## CTR-SYNTAX-001 — Shared compact-script framing

One lexical component owns quoted operands, heredoc framing, decoded command frames,
and comparable target identity for the compact edit syntax in `REQ-SCRIPT-001`. The
engine and rejected-script recovery share that framing but retain separate ownership of
filesystem evaluation, ancestry, correction policy, and output.

The same component separates mixed edit and shell segments without parsing or executing
shell programs. Reserved openers, inline restrictions, and physical source positions
remain lexical facts. Root engine parsing remains edit-only.
