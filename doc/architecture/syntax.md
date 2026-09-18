# Shared compact-script framing

## CTR-SYNTAX-001 — Shared compact-script framing

One shared component owns quoted operands, heredoc framing, decoded command frames,
and comparable target identity for the compact edit syntax in `REQ-SCRIPT-001`. The
engine and rejected-script recovery share that framing but retain separate ownership of
filesystem evaluation, ancestry, correction policy, and output.

The same component separates mixed edit and shell segments. It delegates heredoc word
syntax and closing-boundary recognition to the existing `mvdan/sh` parser, never its
interpreter. A literal synthetic redirection isolates heredoc framing from HPATCH's
JSON-string and target syntax. The adapter bounds parser input and extracts original
physical rows, preserving exact bytes and terminators rather than shell-expanded values.
Mixed-script shell bodies remain opaque program data. Root engine parsing remains edit-only.
