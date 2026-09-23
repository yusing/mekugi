# Journal

## REQ-JOURNAL-001 — Per-thread milestone journals

Mekugi mode owns one durable milestone journal per stable thread. Passthrough is unchanged.
Items have router-assigned IDs (`amber`, `apple`, ...), nonblank UTF-8 text, an optional original
question, canonical author, router sequence creation/update values, `report_now`, `reported`,
and `flushed` state. A thread has at most 256 items; there is no lifetime thread-count or mutation-receipt-count ceiling. Combined
question and text content is limited to 16 KiB per item. The per-response live progress budget
is also 16 KiB. Terminal flushes have a separate bound sized for all 256 items, including labels
and the author heading. Main completion flushes only its own journal; child results are already delivered by native completion. Each journal has its own terminal capacity. Record-size failures identify the limiting byte budget. Initialization errors retain their cause and
reject preparation rather than exposing an uninitialized journal. Journals follow the automatic
session-retention policy in [REQ-ROUTER-001](router.md); active turns and shared inherited records
remain protected.

An ordinary fork copies the source's latest journal at its first accepted normal Responses
request in the selected workspace, then evolves independently. It does not reconstruct the
historical fork point. Routing-session remaps and resumes keep the stable thread's journal.

Eligible non-strict ordinary function tools receive an optional `journal` array. Each mutation is
`add`, `edit`, or `delete`; additions and edits require nonblank text, edits and deletes require
an existing ID, and a malformed array rejects the host call before execution. The array is applied
in order atomically, then removed from executed arguments while the original call remains available
through replay. `report_now` emits a router-owned user-only **Journal update** labelled with
the item ID and author when known. Live updates do not send native inter-agent messages or
enter any agent's provider input. Successful delivery marks that revision reported, not flushed.
It MUST remain eligible for a terminal **Journal flush**, whose heading identifies its known author
and whose entries identify their IDs. A flush containing one item puts its ID in the heading and
renders its text directly, without list indentation; multi-item flushes retain the entry list.
These labels distinguish journals from stock commentary and reasoning summaries without rewriting
stock output. Deletes are silent unless retracting an already-reported ID.

An add/edit mutation may carry `answer: true`, with only the answer in `text`. The router
attaches the latest actual user message or native `NEW_TASK` assignment addressed to the
requesting child from that request's visible history, not instruction or environment context.
Assignments use their plaintext payload, not the routing header. The request's canonical child
name must match both the native recipient and task header, and the header sender must match the
native author. Ordinary inter-agent messages, completion notifications, and assignments to other
agents are not answer sources. A later user message or eligible assignment replaces the earlier
source. An encrypted assignment cannot supply a question and blocks fallback to an older source.
The agent does not repeat the message. Missing or oversized source content rejects the answer
mutation rather than inventing or silently truncating a question.
Omitting `answer` on edit preserves the attached question; false clears it; true attaches the
current source message. Delete, list, and finish reject a direct `answer` operand. The attached
question is retained by list, durable replay, restart, and forks, independently of later user
messages. Inference is request-local; concurrent threads and branches do not share its source.

Mekugi mode also exposes `functions.journal` with one operation: `list`, `add`, `edit`, or
`delete`, or `finish`. List is read-only and may address only a proven ancestor or descendant journal. Unknown
or conflicted ancestry fails closed. Durable workspace identities, not the live activity
collector, authorize relative access after a router restart with only the requesting
thread observed. Authorization and returned items use the same locked snapshot.
Mutations return router-assigned IDs. The dedicated tool includes `journal_ids` for any batched field mutations, independently of its main operation result. Journal calls are
router state operations and do not invoke an executor. The dedicated schema exposes the optional
batched `journal` field. Existing journal declarations anywhere in the tool catalog, including
nested additional-tool namespaces, reject built-in tool exposure.

Direct `functions.journal({"op":"finish","journal":[...]})` requests turn completion,
optionally applying the last atomic mutation array in the same call. Finish takes final mutations
only through `journal`; other operands must be unset or at their empty/default values. Agents call it alone after required tool
results arrive, rather than waiting or generating another final-answer turn. On a successful
response with successful journal results and no client-dispatched calls, it completes and
returns the terminal response without another provider request. Mixed client calls remain
host-dispatched and prevent completion; journal operation error results continue for correction.
Invalid or rejected mutations in a dedicated journal call return an `ok: false` tool result
for correction and prevent its primary operation. Batched fields on host-dispatched tools
still fail translation under the atomic field contract.
Failed, incomplete, or interrupted responses never complete or flush via finish.
Completion intent is response-local: replay, resume, and forks do not finish a new turn.
Code Mode journal publication does not expose finish. The Bash/POSIX finish surface below
binds intent to its originating invocation rather than a response-local direct-tool call.

On a successful explicit main finish with no client-dispatched calls, the router emits token metrics,
then one deterministic flush of its own journal containing only unflushed revisions, including previously
live-reported entries, skipping the flush when empty. The flush remains the last assistant message in
both streamed events and the terminal snapshot so native turn completion does not display it again.
Terminal flushes and terminal retractions render as assistant
`final_answer` messages, not commentary; live updates remain commentary. These terminal messages
are user-visible only and retain the same exact-ID removal from later provider input.
Only successful terminal delivery marks a revision flushed;
edits clear both current-revision delivery flags. `list` exposes both flags. Finish ends the
turn without a follow-up provider request or a separately generated final answer. A child finishes without flushing and emits
`Journal result` with its own current journal items,
including already-flushed items, as the native completion result. The result preserves item
IDs, author, questions, and Markdown using the terminal item renderer and capacity bound.
An empty journal returns `No journal entries.` under the result heading. Completion does not
include journal delivery counts. Result delivery does not mark items
reported or flushed and does not include descendant journals. The parent receives the result
text without a journal lookup or another child provider request.
The child result appends its retained change ranges and one aggregated numstat using
`mchanges --summary` count semantics. Journal and change selection share a locked snapshot.
Selection uses durable executing-thread ownership, falling back to the originating stream
for older records; it excludes other threads' attempts, even within a shared recovery ID.
Ranges identify the retained changes, while counts cover only this child's evaluations.
Like the current journal list, this is thread-wide retained history, not a per-follow-up delta.
Ranges are ordered by stream allocation and numeric ID, split at gaps and the reader's
range limit. No recorded changes is explicit; unavailable or retired evidence is labeled
unavailable rather than zero. Existing terminal capacity and retention requirements apply.
The native completion result is the sole audience payload; the router does not send an
additional completion notification or take over host lifecycle or audience routing.
Live updates remain immediate. Main completion never replays descendant journals, including after router restart.
Child journal revisions remain available through authorized journal reads and subsequent child results.
Failed main delivery leaves its own unacknowledged revisions pending. Failed,
incomplete, and interrupted responses neither flush nor discard already-streamed provider output.
Router-owned messages use generated IDs and are removed from later provider input by exact ID.
Before adding journal results or notices to a streaming terminal with an absent or empty
output snapshot, the router MUST preserve completed streamed items in the projected snapshot.
Internal journal continuations MUST preserve client-dispatched calls and their paired
results in subsequent WebSocket history.
Visible named journal results MUST also invalidate the provider's cached input prefix when
the current workspace has no matching replay record, including a new turn whose workspace
metadata has not arrived yet. Replay sends those standalone results unchanged without
`previous_response_id`; it does not borrow records or filesystem authority from another
workspace. Matching records still restore the exact original call and paired result. A confirmed
provider prefix ending with that call allows the WebSocket reconciler to send only
the missing result and new input; restoration alone does not establish cache validity.

Mekugi mode forces `tools.update_plan.enabled=false` and removes `update_plan` declarations from
the request catalog, including nested additional-tool namespaces. The caller's base instructions
remain unchanged; the journal tool descriptions supply additive guidance. Passthrough retains the
stock tool catalog and prompt.

### Runtime authoring

Journals record checkpoints and milestones, not plans or ongoing narration. Agent guidance
asks for a concise final report containing only distinct, current findings, results,
validation, or blockers, without overlapping progress or superseded summaries.
Parents' own journals cover their results, integration decisions, and actions on findings,
not repetitions or summaries of other agents' journals.
Native child completion preserves each agent's original report; main completion does not repeat it.
Agents mark answer items with `answer: true` and put only the answer in
`text`; the router supplies the original question. Live notices label these as **Question** and
**Answer**; terminal blocks use **Answer** or **Answers**, according to the number of answers.
The router preserves authored Markdown rather than summarizing it. Within each terminal journal
message, all items with an identical question form one block: the question appears once, followed
by all its answers together. Groups follow the first rendered occurrence of their question;
answers retain their journal order within each group. Plain milestones remain separate entries
at their first-occurrence positions, visibly separated from question blocks. Each author journal
and each delivery groups only its own rendered items, after filtering already-flushed revisions
where applicable.
Stored questions remain attached to every answer, and standalone live updates remain self-contained.
Each answer keeps its ID separate from its body and indents all body lines under that item,
including blank lines, nested lists, paragraphs, and fenced code blocks.

Code Mode reserves `await journal({op, id?, text?, answer?, report_now?})`, also accepting
a mutation array. The parser preserves strings, comments, properties, and unrelated
identifiers, and leaves unparseable source unchanged for the executor to diagnose.
A single mutation returns its item ID; a mutation array returns the ordered item IDs.
Nested calls can use an added item's returned ID in a subsequent edit. Publication failures
throw rather than returning an execution envelope as a journal ID.

Code Mode journal mutations are lowered to an authenticated `mjournal`
frontend invoked through stock `tools.exec_command`. The frontend publishes
only the requested mutation through a call-scoped broker capability; it does
not run the surrounding program or replace the stock result. Programmatic
calls can await the returned item ID, including an add followed by an edit.
Failed publication throws. The capability expires with its owning call and
cannot be borrowed by another thread. Agent-authored source and private
publisher credentials are not added to sanitized metrics.

`functions.journal` owns `list`, `add`, `edit`, `delete`, and `finish`. Its
`finish` operation may include a final mutation batch. A successful direct
finish with no pending Codex-dispatched work can deliver the terminal journal
without another provider request. Failed, incomplete, or unrelated tool calls
cannot turn a prior finish into a new terminal result. Replay may restore the
exact journal call and result, but cannot finish another turn or branch. For
Code Mode, finish through the dedicated `functions.journal` tool after required
stock results, not as an executable command.

### Delivery failures

Required terminal journal messages must have retained replay provenance before an
explicit finish can complete successfully. If required retention fails,
the response fails instead of silently completing without the flush or child summary.
Unacknowledged revisions remain available for a later flush.

A provider final message is not a journal finish. The router neither suppresses that text nor
uses it to trigger a journal flush. Ordinary token-usage buffering remains bounded and releases
provider events unchanged on overflow.

### Acceptance

1. Mekugi catalogs omit Tasks/`update_plan` and project journals only onto eligible ordinary
   function schemas. Strict tools, provider-owned additional tools, collaboration, and
   user-messaging schemas stay exact; passthrough is unchanged.
2. Batched mutations apply atomically before execution, return assigned IDs, and disappear
   from host arguments. Replay restores the original call without executing it again.
3. CRUD, independent ordinary forks, resume, capacity rejection, and retained reporting
   revisions survive router restart in the selected workspace.
4. Cross-thread listing allows only proven ancestors and descendants, never siblings.
   It is read-only and does not copy entries or subscribe to their delivery.
5. Immediate notices are acknowledged after successful emission without consuming the terminal
   flush. A failed live or terminal delivery remains eligible for retry. Silent edits become
   flush-eligible again; deleting a previously shown ID with report_now emits a retraction.
6. Successful explicit main finish calls show only main's unflushed revisions,
   including live updates, after eligible token metrics. The flush is emitted exactly once and remains
   the last assistant message. Child finish calls save without flushing and
   retain a nonempty completion result containing their current journal text. Finish makes no
   final-answer continuation request.
   Provider messages remain unfiltered and do not trigger a flush; failures and interruptions do not terminal-flush.
7. Native client normalization preserves journal results and returns the child's
   current journal text after one finishing child request, without a final-answer
   continuation.
8. Debug evidence separates applied mutations, runtime wiring, live rendering, and
   terminal flushing without recording journal bodies or private publication credentials.
9. Multiline Markdown stays within its terminal journal item. Answer items display the
   original question and a labelled answer in live delivery. Terminal delivery displays each
   identical question once per author journal, followed by all its answers in one block. Different
   questions and plain milestones remain separate; groups follow first occurrence and answers
   keep their relative order and full Markdown. Filtering flushed revisions, retry, child
   completion, and restart group only items included in that message. Question edits, clearing,
   list, replay, restart, and forks preserve the specified state semantics.
