# Native orchestration

## REQ-ORCHESTRATE-001 — Isolated batch preparation

Orchestration uses separate Codex threads and checkouts under one Mekugi session.
Native collaboration tools retain their existing behavior and workspace. The
invocation-local `orchestrate` MCP server uses host caller metadata and retained
workspace ownership. Tool arguments cannot select another caller's workspace or
run. User configuration remains unchanged.

Delivery is staged. The first capability is Git checkout preparation and listing
through `tools.mcp__orchestrate__prepare({task_name:"batch"})` and
`tools.mcp__orchestrate__list_agents({})`. Preparation creates no Codex thread and
starts no model turn. Main can copy required ignored inputs into the returned
checkout before a later spawn. Task names contain lowercase letters, digits and
underscores, begin with a letter, and contain at most 64 characters.

Each batch starts on its own branch at the source's committed HEAD. Source index,
uncommitted files and ignored inputs stay in the source checkout. A selected
subdirectory remains the child's working directory inside the new worktree.
Preparation returns the task name, branch, checkout, cwd, baseline and observed
preparation state. Repeating a successfully prepared task returns its retained
record; it never resets the branch or replaces files copied afterward.

The durable run record is scoped to the selected workspace and coordinating
thread. Preparation intent is saved before checkout creation. A failed or
interrupted preparation remains visible; retry does not blindly repeat its
filesystem effects. Listing reads retained facts without starting processes or
claiming that historical preparation state proves current filesystem contents.
Prepared branches and checkouts remain until explicit cleanup; ordinary replay
retention does not remove them.

Acceptance:

1. Two batches have distinct branches and checkouts at the committed baseline;
   source dirty files and index are unchanged.
2. Repeating preparation and reopening the store preserve the same checkout,
   including later copied ignored inputs.
3. Missing or invalid caller identity, invalid task names and non-Git sources
   reject without creating an unowned checkout.
4. Preparation failure retains its intent and failure state; a repeat does not
   retry the effect. Listing another thread cannot reveal the first run.
5. Registration is invocation-local and does not change existing journal tools.

## REQ-ORCHESTRATE-002 — Fresh batch threads

The MCP server exposes `spawn_agent` for a prepared batch and `interrupt_agent`
for its live turn. For example, after preparation:

```js
await tools.mcp__orchestrate__spawn_agent({task_name:"batch",message:"Complete the assigned batch and report its checks."});
```

Children start as independent Codex threads with fresh context. `message` carries
the complete assignment. Main can select model, reasoning effort and service tier;
omitted settings inherit its current effective values. Host-confirmed settings
are authoritative. Native subagents stay inside the child's checkout. Host thread
identity selects their journal workspace. Presentation keeps identical native agent paths in
different thread trees separate, without changing host identifiers. Live native
descendants also prevent coordinator departure.

A launch reserves its prepared checkout before requesting a host thread. Repeats
return retained progress; a changed handoff or launch configuration rejects.
Confirmed thread identity is saved before the first turn, and confirmed turn
identity is saved before success is returned. Uncertain outcomes require
inspection rather than an automatic retry. Launch checks the checkout location,
branch and baseline without resetting prepared inputs.

Cancellation prevents the first turn when possible. Late host acknowledgements
remain useful; an already-started turn is interrupted even if saving its result
fails. An interrupt acknowledgement does not claim the turn has finished: host
lifecycle events supply that outcome. Checkout preparation and orchestration
run-storage waits leave input and host events responsive. Running batches currently
prevent quitting or switching coordinators; confirmation and thread navigation remain below.

The prepared checkout replaces the source repository in runtime workspace roots;
explicit external roots and an empty root list remain inherited. Named permission
profiles and approval-review routing remain intact. Missing profile provenance
rejects launch rather than approximating a custom policy with a sandbox label.

## Accepted delivery scope

The remaining capabilities are accepted but not yet delivered:

- `/orchestrate` starts the workflow or opens its run picker. Instructions appear
  once in the initial input and recover once after context reset.
- `send_message`, `followup_task` and `wait_agent` coordinate run members without
  replacing Codex execution.
- Run-authorized journals mount read-only across checkouts. The new Main-only
  `accepted` task state records reviewed integration; child completion cannot
  accept its own result or complete Main's integration task.
- Thread navigation scopes transcript, Activity, Agents, Journal and Diff to the
  viewed thread. Drafts remain per-thread in memory. Questions and approvals are
  labeled and answered on their originating threads.
- Resume restores run identities and confirmed facts without reviving processes
  or repeating effects. Exit confirms interruption of running children.
- Main integrates native VCS branches serially, preserving coherent commits and
  unrelated edits. Cleanup removes only idle, accepted, clean run-owned checkouts
  at their accepted tips, with no queued work or active native descendants.
- jj and Mercurial use native workspaces. Other sources use run-owned shadow Git
  repositories; conflict detection precedes source writes and partial writeback
  remains recoverable. Observation snapshots do not own durable batch branches.
- Initialized submodules and evidence inputs are prepared before the first turn.
  The home `batch-agent-sessions` skill/helper retires only after parity.

Third-party threads retain their current stable schema exposure during initial
delivery. Child-authored journal roots remain unchanged. Nested orchestration
and versioned shadow-source baselines must
be settled before their affected capability is exposed.
