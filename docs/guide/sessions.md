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
skills and captured-change IDs through native `functions.new_context`, or recover
needed facts through ordinary reads after reset. `wait_agent(compact:true)` resets
before delivering the wait's outcome. Requested resets are not vetoed by a cost estimate.

Compact-and-wait first lets its enclosing execution finish. While that execution
is running or yielded, manual and automatic resets wait rather than interrupt it.
The outcome is delivered only after reset and host execution settle; reset failure
keeps the wait and reports the failure with its eventual outcome.

The latest reload selection also applies to later manual and automatic resets.
Omitting `reload` keeps it; `reload:{sources:[]}` clears it. Temporary task-state
JSON is optional and read only when selected, with selected ranges and nested
references determining what is acquired.

Selected reload and fresh-delegate preload text can include `@path` references to
required files, including inside JSON state. Bare paths do not trigger reads. The
selected source counts as depth 1; depth 3 is the last acquired level. References
resolve beside native source files, or from the workspace for managed skills.
Skills can select a reference such as `name/references/guide.md` instead of the
whole skill. Captured diffs and unselected lines do not expand references.

Before the next model request, native reset acquires selected sources with the thread's
permissions. Failed reads stay visible without blocking the others. Codex supplies
`AGENTS.md`, so it is excluded from reload. Captured reloads survive resume and fork
without rereading merely because the thread resumes; the next reset reads current
sources. Earlier saved sessions retain their compaction summaries, available
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
