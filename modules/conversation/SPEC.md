# Conversation

## Purpose

Own editable and submitted drafts, question and approval presentation, and
bounded side answers independently of rendering and host request lifecycles.

## Scope

The composer API owns text, byte-based caret position, atomic attachment tokens,
undo/redo, local input history, external-editor reconciliation, and draft joining.
Main, question answers, approval reasons, queued input, and side questions use the
same values. Terminal geometry, key decoding, clipboard acquisition, focus, and
process launching remain renderer responsibilities. The application retains live
request handles, submission admission and settlement, and secret admission.
Conversation retains queued compaction intent and payload ordering. Its Codex
binding maps backend-owned identities, offered choices and review facts to neutral
question and approval records; response wire encoding remains backend-owned.

Completion owns byte-based targets, choices, filtering, dismissed targets and
workspace-scoped result admission. Its terminal view owns completion and skill-menu
painting and key interpretation. The application performs host searches, file scans
and skill writes, retaining their cancellation and response handles. Application
menus share list drawing without transferring settings, archive or copy actions to
Conversation.

Detached prompt views exclude draft text, secrets and live handles. Terminal
presentation consumes those values and returns editor or response intents; the
application executes their effects. Key interaction and Main input readiness follow
the [terminal session contract](../../apps/mekugi/internal/terminalsession/SPEC.md#questions-and-approvals).

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

### Questions and approvals

Main-thread questions and approvals belong to one attached session. Their records have
stable identities and detached view values. Displaying a child transcript does not
transfer Main input ownership. Restored questions use the same identities and
resolution rules without opening an editor or issuing a request.

Questions prefer waiting synchronous requests before asynchronous calls. Approvals
are ordered oldest first and take precedence when explicitly opened. Opening either
prompt parks Main's complete editor; hiding, answering, or switching prompts restores
it without merging answer text. Every non-secret answer and denial reason retains
its own editor across navigation and hide/reopen. Only an empty, idle Main composer
can yield automatically. Explicit hiding suppresses automatic reopening until a new
request arrives. Rendering readiness, focus, scroll, and keys belong to the renderer.

Each question offers its named options and free text. Synchronous options can carry
a note. Answering advances to the next unanswered question; submitting with skipped
or incomplete questions requires confirmation. Sending removes a call from actionable
counts but does not claim delivery. Async calls remain open across turn end. An
ordinary prompt committed in a new turn supersedes older open calls; steering does
not. A matching committed reply resolves only its question. Rejection restores
answerability and the parked drafts. External resolution, interruption, or
supersession saves discarded non-secret drafts to local input history; secrets are
discarded. Reply envelopes never become ordinary history or duplicate message bands.

Synchronous calls receive one response with the original host identity. Options,
notes, free text, and skipped questions retain Codex's answer semantics; an
all-skipped call still sends an empty response. Hidden synchronous requests keep
ordinary input queued instead of steering. Async
answers stay batched by call and are committed by thread, item, and question index.
Images and selected-text snapshots accompany the answers through the composer.
Images are numbered across the batch, survive rejection, and remain host-owned after
submission. Sync attachments follow the completed response as a companion input.
Queued replies can be reconciled before dispatch; transmitted input stays host-owned.
Answers steer during an active turn, start a turn when idle, or queue until input
admission permits delivery. They wait for a tool's generated input to complete,
without waiting for execution to finish; ordinary prompts retain instant interruption
and can pass deferred answers. Pending progress distinguishes queued or sending input
from committed delivery. Questions never count as final answers or complete turns.

Approvals from Main and child threads retain host order and scopes. Commands offer
the host's supplied decisions, or accept, a proposed command-prefix amendment, and
cancel when no list is supplied. Network wording follows the requested host scope.
Edits offer once/session approval or cancel and name the matching live item paths.
Permissions offer turn, strict-review turn, session, or an empty grant. Terminal
input requests name the running terminal and quote the proposed input.
Unknown choices leave an unanswerable request visibly blocked. Supported command
denial can carry a reason and continues the turn under the
[backend execution contract](../../backends/codex/SPEC.md#execution-authority);
proxy-free handling preserves the
host decision. External resolution and turn end clear pending approvals without
claiming approval or denial. Approval facts decorate the associated host item rather
than create another transcript event. Auto-review facts use their exact target item.

[Remote-write](../remotewriteapproval/SPEC.md) and
[sudo](../sudoapproval/SPEC.md) policy, grants, and expiry remain in their approval
owners.
Local authentication requests share the secret editor; their answers never enter
neutral draft state, views, answer records, or durable history. Skipping denies
authentication, and retries use a new live authentication request. Live handles, sockets, response
payloads, and secrets stay in adapters. No view or replay fact can revive them.

### Queued and submitted input

Unsent steers precede next-turn input. Ordinary entries join in send order; a
question reply or compaction command remains its own submission. Deferred answers
wait for generated tool input to finish; an ordinary prompt can pass them.
Payload snapshots retain attachment bytes and neutral question identity while
session control settles the corresponding host submission. Committed input is
never restored or resent. Rejected or withdrawn input returns before newer drafts;
a rejected shell command stays separate from prose and can be recalled from history.
Confirmed session replacement preserves waiting input, retires submitted payloads,
and closes the side conversation. Rejected replacement preserves them all.

Submissions carrying attachments or skills, including stacked input and encoded
snapshots, must fit a conservative 1 MiB UTF-8 text budget; larger input returns
unsent with an actionable notice. Ordinary text limits remain Codex-owned. This
bounds attachment growth, not the context size of an arbitrary existing conversation.

### Side questions

`/btw QUESTION` opens a temporary conversation without changing Main's input,
execution, transcript or settings. Follow-ups reuse its host snapshot and replace
the displayed exchange. Pending Main input settles before the first snapshot;
an already-running turn continues. Later Main activity does not enter follow-ups.
The snapshot uses Main's effective model, reasoning effort and service tier.
Codex owns its ephemeral history and context. The answer requests no tools,
disables environment access, and uses read-only permissions without approvals;
Codex does not guarantee that non-environment tools are unavailable.

A running answer refuses another side submission without consuming the draft.
Failures stay with the side answer. Rejected input and attachments return ahead
of newer Main input. If Codex requires compaction, Main stays untouched and the
side conversation refuses follow-ups: compact Main, close the side answer, and
retry with a new snapshot. This follows Codex's decision, not a context estimate.
Side events never enter Main's questions, roster, notifications or transcript.

Closing discards the visible exchange and cancels only its side turn. Delayed
fork/start responses finish cleanup without reviving the view or replacing a
newer side answer. The transient display retains at most 256 KiB across 256 items
and reports truncation; Codex retains the full context. Side answers are neither
persisted nor restored on resume.

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

Application-owned session stores retain conversation queues, submitted payloads and
side presentation under the composed service. Dropping a scope cannot revive host
requests. One attached-session editor owns the active draft and local input history. Parked
editors and submitted payloads are detached values. Clipboard image and managed-skill process acquisition, plus image cleanup, stay
with the session adapter. Cleanup accounts for active, parked, queued,
undoable, and side-question drafts before reclaiming its unsubmitted files. User-owned
files are never reclaimed; submitted image files remain available for host resume.

## Codex connection

The application sends composed payloads through session control. Codex owns turn
execution, acknowledgements and durable input history. Image bytes and framing remain
host-owned. Terminal painting consumes bound byte spans; source text remains unchanged.

### File and skill acquisition

Completion replaces the whole token around the caret, retaining its byte span even
when the caret is inside the token. Bound attachments remain opaque. Dismissing a
target keeps it closed until the target changes; changed queries retain only matching
results. Choosing a file or skill preserves its exact identity through the ordinary
composer undo and attachment lifecycle.

The application roots completion in the active thread's absolute workspace, never
the router directory. Ordinary file lookup uses Codex's ignore-aware search. `@!`
uses a cancellable, debounced read-only scan to include ignored files, excluding
VCS metadata and directory-symlink traversal. Pending updates coalesce; stale or
cancelled results cannot replace another workspace's current selection.

Enabled, exact, unambiguous `$name` references bind at a word boundary or submission,
including pasted input. Unknown, disabled, ambiguous names and shell variables remain
literal. A pending catalog lookup delays submission; failure reports unavailable
references without suppressing available skills from another source.

When available in Mekugi mode, `skills-mgr list --codex` runs in that workspace with
the wrapped environment. Managed names take precedence over same-named Codex entries.
Submission/queueing resolves them with `skills-mgr get --codex NAME`, including
explicitly invoked skills omitted from the managed model catalog. Native entries
retain their exact metadata path, including placeholders. Repeated names attach
once. Instructions use ordinary snapshot budgets/envelopes and project as user
input, never a synthetic native path or name-only replacement.

Replay recovers immutable instructions and identity from submitted envelopes without
rerunning the manager. Recalled references read current content when submitted again.
Host text elements retain file/skill byte ranges relative to each text part; native
skill metadata can restore unambiguous bindings when elements are absent. Explicit
elements take precedence for repeated labels; absent metadata leaves lookalikes
literal. Oversized/unreadable instructions use omission receipts, never truncation.

`/skills` offers Codex's List and Enable/Disable actions. Listing inserts `$`;
management filters enabled/disabled entries by display/canonical name. Selecting a
toggle saves its native path; enabled state changes only after success, failures stay
visible, and closing reloads the catalog. Managed references never enter
`skills/config/write` with invented paths; their configuration belongs to the manager.
Closing management leaves an already-sent save pending. Its result updates only the
matching workspace catalog; a late failure remains a notice without replacing the
new query's error or a reopened manager's error.
Plugin browsing is outside this picker. Terminal completion and editing controls
belong to [terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md#composer-controls).

## Records

Conversation writes no durable records. Submitted attachment envelopes are retained
by the host with the user input; local edit history remains attachment-lifetime state.

## Failure

Invalid UTF-8 or ambiguous image/selection placeholders leave an externally edited
draft unchanged. Attachment acquisition failures remain explicit omissions rather than
successful attachments. A rejected host submission preserves its editable payload.

## Acceptance

Neutral tests cover mixed-token edits, grapheme boundaries, undo/history isolation,
external-editor reconciliation, joining, question commitment/rejection, approval
choice mapping, detached views, secret exclusion, completion targets, result admission
and skill filtering. Router acceptance exercises the same owner
through terminal input, queue restoration, question and approval editors, side questions,
attachment submission and replay. Terminal snapshots remain renderer-owned.
