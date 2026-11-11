# Sessions and recovery

Resume, branch and rewind conversations, and understand compaction and retained storage.

## Resume

`resume THREAD_ID` attaches to that thread if it is still running on the local
server, or restores its saved session otherwise. `resume --last` selects the latest
saved session; bare `resume` opens the
workspace picker. Type to search, Tab for all workspaces, Enter to resume. Inside an
idle session, `/resume` opens the picker or `/resume THREAD_ID` switches directly. Saved
model/effort/tier, layout, roster, child history, and retained Diff return; explicit
`-m`/`-c` overrides named settings. Drafts, selections, filters, and scroll positions do
not return. See [resume details](../../apps/mekugi/internal/sessionpresentation/SPEC.md#resume-and-switching).

While idle, `/fork` takes no arguments and creates a saved branch of the completed
conversation, then switches to it with current settings and pending input. The
original stays resumable and workspace files remain unchanged. A failed fork
keeps the original session usable.

`/rewind` opens a user-turn dialog while idle, after the first user turn. Select
a turn to remove it and all later conversation from the same session, keeping
current settings and pending input. It requires paginated Codex history; legacy
sessions are refused before edits change.

Choose Keep edits (the default) or Revert captured edits from that turn onward,
including descendant agents. Without such captures, rewind skips this choice.
Revert uses `mchanges` and preserves unrelated edits through its normal merge
behavior. Missing evidence disables Revert; conflicts stop the rewind. Keep
remains available. Retained change/output evidence stays available, including
any recorded reversal. If conversation rewind fails after edits were reverted,
inspect the reversal output before deciding what to recover.

Rewind keeps the session identity and descendants. Codex reloads the caller's
runtime, so its process handles may stop working. No saved branch or archive
confirmation is created.

## Compaction and recovery

Manual `/compact`, context-full compaction and agent checkpoints reset Codex
context without generating a handover summary. Agents may select files, line ranges,
skills and captured-change IDs with a checkpoint, or recover needed facts through
ordinary reads after reset. Temporary task-state JSON is optional and read selectively,
never automatically loaded in full. Claude delegates retain native compaction.

At measured context usage of 40% or below, agent checkpoints estimate the next
uncached input cost and skip reset when it
would exceed the latest observed input cost, including that request's cache discount.
This compares one next input, not the cost of all remaining work. Selected sources
are not preflighted for cost above 40% or when context usage is unavailable; those
checkpoints bypass the cost veto. Otherwise, selected sources
may be read for this check, but are not attached unless reset succeeds.
The estimate assumes no cache hits after reset and unchanged source files;
unavailable cost evidence permits reset with a notice. A preflight that produces no
usable capture skips reset rather than retrying denied or canceled acquisition.
The transcript immediately shows **Compaction skipped** with the reason and states
that context was not reset and reload sources were not attached. A skipped reset
preserves the wait and its deadline; the eventual model-facing outcome includes the
same reason. Manual `/compact` and context-full compaction bypass this cost check.

Selected reload and fresh-delegate preload text can include `@path` references to
required files, including inside JSON state. Bare paths do not trigger reads. The
selected source counts as depth 1; depth 3 is the last acquired level. References
resolve beside native source files, or from the workspace for managed skills.
Skills can select a reference such as `name/references/guide.md` instead of the
whole skill. Captured diffs and unselected lines do not expand references.

Before the next model request, Mekugi acquires selected sources with the thread's
permissions. Failed reads stay visible without blocking the others. Codex supplies
`AGENTS.md`, so it is excluded from reload. Captured reloads survive resume and fork
without rereading. Earlier saved sessions retain their compaction summaries, available
from **Context compacted** in the transcript. Captured changes and output remain
readable while their [storage](#replay-storage) is retained. See the
[compaction contract](../../modules/agentguidance/SPEC.md#compaction-context), including
the native caller-prompt-file startup limitation.

## Replay storage

Records live in `$XDG_STATE_HOME/mekugi/replay`, default `~/.local/state/mekugi/replay`.
Replay never reruns commands/restores processes. Data is capped at **4 GiB**, removed
after **14 inactive days**; capacity cleanup removes oldest inactive sessions, never
running work. **Codex chats/workspace files are never deleted.** Cleanup invalidates
references; power loss can lose recent records. If retention fails, free space and retry
retention, not the operation. Stop wrappers before moving storage aside. Use `mekugi
inspect-storage`; see [storage details](../../modules/sessionhistory/SPEC.md).
