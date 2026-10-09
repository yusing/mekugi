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

## Accepted delivery scope

The remaining capabilities are accepted but not yet delivered:

- `/orchestrate` starts the workflow or opens its run picker. Instructions appear
  once in the initial input and recover once after context reset.
- `spawn_agent` starts or forks a prepared checkout's Codex thread. Main selects
  role, model, reasoning effort and service tier; host-confirmed settings are
  authoritative. Native subagents remain inside their parent's checkout.
- `send_message`, `followup_task`, `wait_agent`, `list_agents` and
  `interrupt_agent` coordinate run members without replacing Codex execution.
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
delivery. Child-authored journal roots remain unchanged. Partial-history fork
compatibility, nested orchestration, and versioned shadow-source baselines must
be settled before their affected capability is exposed.
