import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";

export function createMChangesTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mchanges",
      description: "Review, revert, or reapply captured filesystem changes from stock apply_patch, shell file operations, and supported Python/JS writes; not a Git diff or provisional preview. Usage: `mchanges --list [--workspace DIR] [--max-tokens N] | mchanges [--mine | ID[..ID] ...] [--summary|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...] | mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]`. Reuse the change IDs supplied with completed edit receipts; do not run --list when those IDs are already available. Use `mchanges --list` only to recover missing IDs. For handoffs and review requests, give explicit mchanges IDs or same-agent inclusive ranges and the review scope, not vague ‘diff’, ‘pending changes’, or ‘pending diff’ labels. Bare mchanges, --mine, and --list are scoped to the calling thread, not whole-workspace coverage; --list compresses the caller's IDs with counts; --summary shows diff-pane file statuses and per-path counts. --net composes selected captured diffs, not a live Git diff. Choose editing tools for the task, not capture support. Use mchanges for captured changes. An empty caller-scoped list or a missing completion range does not establish capture failure: obtain the author’s explicit range and inspect it before falling back to Git. Use a scoped workspace diff or file inspection only for identified uncovered paths or missing baselines. Avoid duplicate reviews of the same evidence; skip --summary before an already-needed diff. `revert` undoes and `apply` replays selected changes in the workspace, git-style: drifted regions merge; overlapping edits leave conflict markers (exit 1). Output states each file relative to mchanges history, not Git (`clean`, `+N -N`, `UU` conflict, `??` unknown). The revert is itself recorded as a change; follow the printed undo line. Recorded diffs are historical evidence, not proof of current workspace contents.",
    },
    nativeExecutor: "mchanges",
  };
}
