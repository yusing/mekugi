# Observed changes and live view

## REQ-CHANGES-001 — Durable review of stock edits

Mekugi observes stock Codex `apply_patch` calls. Codex executes each call once;
Mekugi does not replace the tool, run a hook, apply a second patch, or alter the
argument or result. A complete argument may be projected as a provisional live
diff while streaming. Only the actual host result and resulting workspace
state determine a completed change record. For a Code Mode cell, outer script
completion alone does not prove a nested patch succeeded, even when file effects
are visible, so nested application remains unconfirmed. A cell that yields
remains unfinished until its host wait result is terminal.

The observer captures bounded pre-edit UTF-8 contents for paths named by a
complete patch. It later compares those snapshots with the resulting files.
If source cannot be captured completely, the affected review entry is marked
incomplete instead of inventing a diff. A successful host result may produce an
applied or no-op record. A failed result is never reported as applied, even if
some files changed; the visible partial difference remains reviewable and the
failure is retained in history. An unfinished call has no completed record.
Storage failure must not expose dependent review evidence as durable.

Each completed observed call receives a short session-scoped change ID. Root
and child agents share an inherited namespace; forks and side threads receive
an isolated copy of visible records, and resume can read durable records after
a fresh router process. Retention may expire inactive records according to
[REQ-ROUTER-001](router.md). Replay reads retained facts and never repeats a
host edit.

### Bounded read command

The session-private `mchanges` executable lists the current thread's change
IDs or reads selected IDs and ranges:

```text
mchanges --list [--workspace DIR] [--max-tokens N]
mchanges ID[..ID] ... [--summary|--history] [--workspace DIR] [--max-tokens N] [-- PATH ...]
```

`--list` includes pending and completed IDs owned by the calling thread, but
does not expose sibling threads' IDs. Explicit IDs can still be read across
agents in the shared namespace. The default view shows each status and unified
file diff. `--summary` gives added and removed line counts by path across
selected records. It is not a net workspace diff. `--history` includes the
original observed patch input and
host result. Paths after `--` filter review files without re-reading the
current filesystem. `--workspace ..` selects the owning workspace index when
the command runs from a subdirectory; paths after `--` only filter entries in
the selected record. `--max-tokens` bounds displayed output. When output is
omitted, an `mread` reference retrieves the retained remainder. Pending, expired, or incomplete
history is explicit; no missing evidence becomes an empty successful diff.
For a Code Mode cell, an observed workspace effect remains application
unconfirmed because outer JavaScript completion does not prove the nested
`apply_patch` result. Direct stock patch success requires both its successful
host result and a complete workspace observation.

A confirmed successful edit can publish a generated `Create` or `Edit`
commentary summary. Classification uses the same observed review files as
`mchanges`, including whether a path existed before the edit. Failed and
unfinished calls do not publish success commentary. These messages are
user-only presentation, not a tool result or application receipt.

### Live terminal view

When an interactive Herdr pane is available, Mekugi opens the viewer on the
first observed editing or execution call. The stream view shows concurrent
main-agent and child calls, and can display provisional `apply_patch` and
stock `cat` heredoc diffs before completion. Interpreter programs can be
shown in their own language rather than as a shell wrapper. A preview does
not claim that Codex ran or accepted an edit. A Code Mode patch held in an
immutable top-level literal binding is rendered as the patch preview; its
escaped JavaScript source is not exposed as a streaming script while the
patch is incomplete.

The saved diff view uses completed observed patch outcomes. It includes
changes from children that are visible to the parent. The viewer switches
to it after the root's usage and journal flush, and back to stream for the
next prompt. The user can switch, scroll, pause following, resume, or flush
visible cards without changing execution or durable evidence. Each mode's
footer reports what the other holds: live calls from the diff view, and
unreviewed files from the stream view. A child caller's card label uses the
same color as that agent in the agents pane.

Subagent activity uses a separate Mekugi agents pane, specified in
[REQ-COMMENTARY-001](commentary.md). If a live diff pane is open, the agents
pane opens below it; otherwise it opens beside the caller, and a diff pane that
opens later goes below it. A failed or closed pane leaves the other one open.

Acceptance:

1. Direct and Code Mode patch calls retain exact stock arguments and results.
2. A streaming preview appears before completion but does not create success
   evidence or a change ID before the host result.
3. Successful, failed, no-op, and partial outcomes are distinguished by
   result and workspace state. Read failures remain visibly incomplete.
4. `mchanges` lists the caller's IDs and reads completed records by ID, range, path, summary, and history
   through the authenticated frontend, with bounded `mread` continuation.
5. Child handoff and resume use durable ownership, not a live process; replay
   never executes an edit again.
6. Live `cat` and interpreter projections are presentation only and preserve
   stock PTY, yield, result, and `write_stdin` behavior.
