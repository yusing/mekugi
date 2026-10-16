# Conversation

Conversation owns editable drafts, attachment identity, question and approval
records, queued payload ordering and bounded side answers. Main, question answers,
approval reasons, queued input and side questions share the composer model.

[Terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md) owns geometry,
focus, keys, clipboard acquisition and external-editor handoff; feature views paint
Conversation values. Detached prompt views contain no draft text, secrets or live
handles. The application admits
input, retains live requests and performs acquisition; [session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md)
issues and settles host operations. Codex owns execution, acknowledgements and
durable input history; the backend owns response encoding. Displaying a child
transcript does not transfer Main's input or prompt ownership.

## Drafts and history

The composer retains text, a byte-based caret, attachment bindings, undo/redo and
local input history. File references, skill references, selected-text mentions and
image placeholders are atomic during navigation and deletion. Explicit bindings,
never matching ordinary text, supply their identity. Character and word operations
respect Unicode graphemes, including combining marks beside attachments. Images
are numbered in draft order after insertion, deletion, joining or external editing.

Undo groups a typed word, a repeated deletion run, a paste, an attachment, a clear
or an editor save. It retains at most 100 edits and resets on submission. Local
input history retains at most 100 entries per thread. Browsing restores the saved
draft and caret after the newest entry. Live history keeps attachment bindings;
resume hydrates from host history without another durable input store. Main and
answer drafts do not survive process restart.

Joining drafts preserves send order, separates nonempty entries with newlines,
retains immutable attachment snapshots and keeps the last entry's caret. Rejected
or withdrawn input can return before newly typed text without losing attachment
identity. Application-only payload metadata stays outside draft state.

External editing preserves exact image and selection placeholders when moved or
removed. Invalid UTF-8 or ambiguous image/selection placeholders, including
duplicates, leave the draft unchanged. File bindings survive unrelated edits, and
repeated labels retain their distinct paths in order. Extending a filename does not
bind its old prefix. Skills bind only complete references.

## References and attachments

### File and skill acquisition

Completion replaces the whole token around the caret, retaining its byte span even
when the caret is inside the token. Bound attachments remain opaque. Dismissing a
target keeps it closed until the target changes; changed queries retain only
matching results. Choosing a file or skill preserves its exact identity through
ordinary undo and attachment handling. Terminal completion and management controls
follow [composer controls](../../apps/mekugi/internal/terminalsession/SPEC.md#composer-controls).

The application roots acquisition in the active thread's absolute workspace,
never the router directory. Ordinary file lookup uses Codex's ignore-aware search.
`@!` includes ignored files, excluding VCS metadata and directory-symlink traversal.
Cancelled or stale results cannot replace another workspace's current selection.

Enabled, exact, unambiguous `$name` references bind at a word boundary or submission,
including pasted input. Unknown, disabled or ambiguous names and shell variables
remain literal. A pending catalog lookup delays submission; failure reports
unavailable references without suppressing available skills from another source.

When available, `skills-mgr list --codex` runs in that workspace with the wrapped
environment. Managed names take precedence over same-named Codex entries.
Submission or queueing resolves them with `skills-mgr get --codex NAME`, including
explicitly invoked skills omitted from the managed model catalog. Native entries
retain their exact metadata source path through editing and history. Managed
placeholders retain their metadata identity while acquisition reads the manager's
instructions rather than placeholder text. Repeated names attach once.

`/skills` offers Codex's List and Enable/Disable actions. Listing inserts `$`;
management filters enabled/disabled entries by display or canonical name. Selecting
a toggle saves its native path. Enabled state changes only after success, failures
stay visible, and closing reloads the catalog. Managed configuration belongs to the
manager; it never uses invented native paths. Closing leaves an already-sent save
pending. Its result updates only the matching workspace catalog; a late failure
remains a notice without replacing a new query's or reopened manager's error.
Plugin browsing is outside this picker.

### Attachment snapshots

Submission and queueing snapshot selected text files and directories from the
active thread's absolute workspace. Repeated paths attach once per draft. Frames
carry source paths and byte ranges, prefer line boundaries and split long lines
only at UTF-8 boundaries. Contents remain user input, never developer or system
instructions. Queued payloads and host history retain the submitted bytes across
fork, resume and provider changes without reopening their source. Recalled editable
bindings take a fresh snapshot when submitted again.

Native and managed skill instructions use the same framing, budgets and omission
receipts as files, never a synthetic native path or name-only replacement. Replay
recovers immutable instructions and identity without rerunning the manager.
Host text elements retain file/skill byte ranges relative to each text part;
native skill metadata can restore unambiguous bindings when elements are absent.
Explicit elements take precedence for repeated labels; absent metadata leaves
lookalikes literal. Oversized or unreadable instructions produce omissions rather
than truncated instructions.

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

Submissions carrying attachments or skills, including stacked input and encoded
snapshots, must fit a conservative 1 MiB UTF-8 text budget. Larger input returns
unsent with an actionable notice. Ordinary text limits remain Codex-owned. This
bounds attachment growth, not the context size of an existing conversation.

## Questions and approvals

Main-thread questions and approvals belong to one attached session, with stable
host identities and detached views. Restored questions retain their identities and
resolution rules without opening an editor or issuing a request. Views and replay
facts cannot revive live requests.

Questions prefer waiting synchronous requests before asynchronous calls. Approvals
are ordered oldest first and take precedence when explicitly opened. Opening either
prompt parks Main's text, caret, undo/redo and history navigation. Hiding, answering
or switching prompts restores Main without merging answer text. Every non-secret
answer and denial reason retains its own editor across navigation and hide/reopen.
Only an empty, idle Main composer can yield automatically. Explicit hiding
suppresses automatic reopening until a new request arrives. Visible readiness and
interaction follow [questions and approvals](../../apps/mekugi/internal/terminalsession/SPEC.md#questions-and-approvals).

### Questions

Each question offers its named options and free text. Synchronous options can carry
a note. Answering advances to the next unanswered question; submitting with skipped
or incomplete questions requires confirmation. Sending removes a call from
actionable counts without claiming delivery. Async calls remain open across turn
end. An ordinary prompt committed in a new turn supersedes older open calls;
steering does not. A matching committed reply resolves only its question. Rejection
restores answerability and the parked drafts.

External resolution, interruption or supersession saves discarded non-secret
drafts to local input history; secrets are discarded. Secret answers and question
reply envelopes never enter ordinary history. Reply envelopes do not create
duplicate message bands. Questions never count as final answers or complete turns.

Synchronous calls receive one response with the original host identity. Options,
notes, free text and skipped questions retain Codex's answer semantics; an
all-skipped call still sends an empty response. Hidden synchronous requests keep
ordinary input queued instead of steering. Async answers stay batched by call and
are committed by thread, item and question index.

Images and selected-text snapshots accompany answers. Images are numbered across
the batch, survive rejection and remain host-owned after submission. Sync
attachments follow the completed response as a companion input. Queued replies
can be reconciled before dispatch; transmitted input stays host-owned. Pending
progress distinguishes queued or sending input from committed delivery.

### Approvals

Approvals from Main and child threads retain host order and scopes. Commands offer
the host's supplied decisions, or accept, a proposed command-prefix amendment and
cancel when no list is supplied. Network wording follows the requested host scope.
Edits offer once/session approval or cancel and name the matching live item paths.
Permissions offer turn, strict-review turn, session or an empty grant. Terminal
input requests name the running terminal and quote the proposed input.

Unknown choices leave an unanswerable request visibly blocked. Supported command
denial can carry a reason under the [backend execution contract](../../backends/codex/SPEC.md#execution-authority).
External resolution and turn end clear pending approvals without claiming approval
or denial. Approval facts decorate the associated host item rather than create
another transcript event. Auto-review facts use their exact target item.

[Remote-write](../remotewriteapproval/SPEC.md) and [sudo](../sudoapproval/SPEC.md)
approval owners define their policy, grants, expiry and authentication behavior.
Local authentication uses a separate secret editor; answers never enter neutral
draft state, views, answer records or durable history. Live handles, sockets,
response payloads and secrets stay in adapters.

## Queued and submitted input

Unsent steers precede next-turn input. Ordinary entries join in send order; a
question reply or compaction command remains its own submission. Conversation
retains queued compaction intent, not its execution lifecycle.

Answers steer during an active turn, start a turn when idle or queue until input
admission permits delivery. Deferred answers wait for a tool's generated input to
finish, not for execution to finish. Ordinary prompts retain instant interruption
and can pass deferred answers. Payload snapshots retain attachment bytes and
neutral question identity until [session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md#submission-and-interruption)
settles submission. That owner also defines interruption, rejection, native-shell
recovery and session replacement. A rejected or withdrawn payload remains editable
and returns before newer drafts; committed input is never restored or resent.

## Side questions

`/btw QUESTION` opens a temporary conversation without changing Main's input,
execution, transcript or settings. Follow-ups reuse its host snapshot and replace
the displayed exchange. Pending Main input settles before the first snapshot;
an already-running turn continues. Later Main activity does not enter follow-ups.
The snapshot uses Main's effective model, reasoning effort and service tier.
Codex owns its ephemeral history and context. The answer requests no tools,
disables environment access and uses read-only permissions without approvals;
Codex does not guarantee that non-environment tools are unavailable.

A running answer refuses another side submission without consuming the draft.
Failures stay with the side answer. Rejected input and attachments return ahead
of newer Main input. If Codex requires compaction, Main stays untouched and the
side conversation refuses follow-ups: compact Main, close the side answer and
retry with a new snapshot. This follows Codex's decision, not a context estimate.
Side events never enter Main's questions, roster, notifications or transcript.

Closing discards the visible exchange and cancels only its side turn.
[Session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md#session-replacement-and-exit)
retains delayed host correlation and cleanup without reviving a closed view or
replacing a newer side answer. The transient display retains at most 256 KiB across
256 items and reports truncation; Codex retains the full context. Side answers are
neither persisted nor restored on resume.

## Lifetime

Conversation writes no durable records. Submitted attachment envelopes remain
with host user input; local edit history remains attachment-lifetime state. One
attached-session editor owns the active draft and local history; parked editors
and submitted payloads are detached values.

Clipboard image and managed-skill acquisition, plus image cleanup, remain with the
session adapter. Cleanup accounts for active, parked, queued, undoable and
side-question drafts before reclaiming unsubmitted files. User-owned files are
never reclaimed; submitted image files remain available for host resume.

## Acceptance

Neutral checks cover mixed-token and grapheme edits, undo/history isolation,
external-edit failures, joining, question commitment/rejection, approval choices,
detached views, secret exclusion, completion admission and skill filtering.
Application acceptance covers queue restoration, prompt editors, side questions,
attachment acquisition/submission and replay. Terminal snapshots remain
renderer-owned; offline prompt checks do not establish host delivery.
