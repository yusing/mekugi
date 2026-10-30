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
not return. Third-party `exec resume`/`exec
fork` uses the mode's default model unless given `-m`. See [resume
details](../../apps/mekugi/internal/sessionpresentation/SPEC.md#resume-and-switching).

While idle, `/fork` takes no arguments and creates a saved branch of the completed
conversation, then switches to it with current settings and pending input. The
original stays resumable and workspace files remain unchanged. A failed fork
keeps the original session usable.

`/rewind` opens a user-turn dialog while idle, after the first user turn. Select a turn to branch immediately
before it, excluding that turn and later conversation while keeping current
settings and pending input. Choose Keep edits (the default) or Revert captured
edits from that turn onward, including descendant agents. When there are no such
captured edits, rewind skips this choice. Revert uses `mchanges`
and preserves unrelated edits through its normal merge behavior. Missing evidence
disables Revert; conflicts stop that rewind. Keep remains available. Retained
change/output evidence stays available, including any recorded reversal.
After switching, a separate confirmation offers to archive the original thread;
No is selected by default. Declining keeps both sessions. Archive failure leaves
the new branch usable and the original available.

## Compaction and recovery

Manual `/compact`, context-full compaction and agent checkpoints run one ordinary
turn on the selected provider. The model writes a handover summary and a separate
selection of files, line ranges, skills and captured-change IDs. Before the next
model request, Mekugi reads those sources with the thread's own permissions and
supplies their complete current contents; a denied or failed read stays visible
without blocking the others. Codex already supplies `AGENTS.md`, so it is never
reloaded. Reloaded context survives resume and fork without another read. Click
**Context compacted** in the transcript to read the retained summary. Captured changes and retained
output remain readable while their [storage](#replay-storage) is retained. See the
[compaction contract](../../modules/agentguidance/SPEC.md#compaction-context).

## Replay storage

Records live in `$XDG_STATE_HOME/mekugi/replay`, default `~/.local/state/mekugi/replay`.
Replay never reruns commands/restores processes. Data is capped at **4 GiB**, removed
after **14 inactive days**; capacity cleanup removes oldest inactive sessions, never
running work. **Codex chats/workspace files are never deleted.** Cleanup invalidates
references; power loss can lose recent records. If retention fails, free space and retry
retention, not the operation. Stop wrappers before moving storage aside. Use `mekugi
inspect-storage`; see [storage details](../../modules/sessionhistory/SPEC.md).
