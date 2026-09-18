# Rejected-script recovery

## CTR-CORRECT-001 — Rejected-script recovery

The shell worker owns recovery-handle lookup, command mappings, correction parsing,
limits, diagnostics, and complete-script reevaluation. Durable records identify
immutable rejected attempts within the execution directory. Recovery does not depend
on router-local state or mutable latest-call selection.

The worker rebuilds corrected text through the generic bounded text editor and sends
the result through the ordinary engine application path. The engine and public library
APIs have no recovery history or recovery mode. Comparable target identity prevents a
target-only correction from resubmitting the same target under different spelling.

The replay store retains attempts and review evidence. It never executes edits during
replay. Native process continuation remains Codex-owned and separate from edit recovery.
