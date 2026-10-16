# Conversation composition

## Purpose

Own editable conversation drafts and their attachment identity independently of
terminal rendering and host request lifecycles.

## Scope

The composer API owns text, byte-based caret position, atomic attachment tokens,
undo/redo, local input history, external-editor reconciliation, and draft joining.
Main, question answers, approval reasons, queued input, and side questions use the
same values. Terminal geometry, key decoding, clipboard acquisition, focus, and
process launching remain renderer responsibilities. The application retains host
question identities, compaction intent, submission ordering, and secret admission.

## Behavior

File references, skill references, selected-text mentions, and image placeholders
are atomic during navigation and deletion. Their identity comes from explicit
bindings, never from matching ordinary text. Character and word operations respect
Unicode graphemes, including combining marks adjacent to attachments. Images are
numbered in draft order after insertion, deletion, joining, or external editing.

Undo groups a typed word, a repeated deletion run, a paste, an attachment, a clear,
or an editor save. It retains at most 100 edits and resets on submission. Local
input history retains at most 100 entries per thread. Browsing restores the saved
draft and caret after the newest entry. Live history keeps attachment bindings;
resume hydrates from host history without creating another durable input store.

Joining drafts preserves send order, separates nonempty entries with newlines,
retains immutable attachment snapshots, and keeps the last entry's caret. Rejected
or withdrawn input can therefore return before newly typed text without losing
attachment identity. Application-only payload metadata stays outside draft state.

External editing preserves exact image and selection placeholders when moved or
removed; duplicate placeholders reject the edit. File bindings survive unrelated
edits and repeated labels retain their distinct paths in order. Extending a filename
does not bind its old prefix. Skills bind only complete references.

Question and approval editors park text, caret, undo/redo and history navigation
without merging answer text into Main. The application excludes secret answers and
question-reply envelopes from ordinary input history and decides when discarded
non-secret answers are saved. Main and answer drafts do not survive process restart.

### Attachment snapshots

Submission and queueing snapshot selected text files and directories from the
active thread's absolute workspace. Repeated paths attach once per draft. Frames
carry source paths and byte ranges, prefer line boundaries, and split long lines
only at UTF-8 boundaries. Snapshot contents remain user input, never developer or
system instructions. Queued payloads and host history retain the submitted bytes
across fork, resume and provider changes without reopening their source. Recalled
editable bindings take a fresh snapshot when submitted again.

Native skills retain the exact metadata source path through editing and history.
Their actual instructions use the same framing, budget and omission lifecycle as
files. Managed placeholders retain their metadata identity while the adapter
acquires the instructions from the manager instead of attaching placeholder text.

Each draft has a 192 KiB encoded attachment budget, half reserved for omission
notices. File reads are bounded to 96 KiB and content frames to 24 KiB. Unreadable,
non-regular/non-directory, non-UTF-8, NUL-containing, oversized and over-budget
sources produce explicit omission frames; the composer reports the first omission.

Directories attach deterministic name-only trees, never recursive file contents.
Root children are depth 1 and their children depth 2. Deeper descendants and child
symlink targets are not traversed. Ancestor and nested `.gitignore` rules apply,
with ancestor lookup stopping at the nearest Git root. VCS metadata remains
excluded even for an ignored-picker reference. Git global excludes and repository
info/exclude do not apply.

Trees list at most 256 entries and 16 KiB while scanning at most 4,096 entries,
including ignored names. A listing exceeding the remaining scan allowance is
omitted whole rather than selecting a filesystem-order subset. Incomplete trees
carry an explicit truncation marker; a fully filtered tree is identified as empty.
Ignore input is bounded to 64 KiB across at most 64 ancestor levels and visited
children. Unavailable ignore rules explicitly omit the affected listing. Directory
snapshots share file envelopes, receipts and content dialogs.

Selected-text mentions follow file frames in the same envelope, preserving their
label and optional source path. Selections over 96 KiB are refused with a notice.
A quote exceeding the remaining envelope budget becomes an explicit omission
frame, reported by the composer. Question answers use the same bound tokens and
quoted attachments as Main.

## Lifecycle

One attached-session editor owns the active draft and local input history. Parked
editors and submitted payloads are detached values. Clipboard image and managed-skill process acquisition, plus image cleanup, stay
with the session adapter. Cleanup accounts for active, parked, queued,
undoable, and side-question drafts before reclaiming its unsubmitted files. User-owned
files are never reclaimed; submitted image files remain available for host resume.

## Codex connection

The application sends composed payloads through session control. Codex owns turn
execution, acknowledgements and durable input history. Image bytes and framing remain
host-owned. Terminal painting consumes bound byte spans; source text remains unchanged.

## Records

The composer writes no durable records. Submitted attachment envelopes are retained
by the host with the user input; local edit history remains attachment-lifetime state.

## Failure

Invalid UTF-8 or ambiguous image/selection placeholders leave an externally edited
draft unchanged. Attachment acquisition failures remain explicit omissions rather than
successful attachments. A rejected host submission preserves its editable payload.

## Acceptance

Neutral tests cover mixed-token edits, grapheme boundaries, undo/history isolation,
external-editor reconciliation, and joining. Router acceptance exercises the same owner
through terminal input, queue restoration, question and approval editors, side questions,
attachment submission and replay. Terminal snapshots remain renderer-owned.
