import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";

export function createMChangesTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mchanges",
      description: "Review, revert, or reapply completed observed evidence from stock apply_patch, shell file operations, and supported Python/JS writes; not a Git diff or provisional preview. Usage: `mchanges --list [--workspace DIR] [--max-tokens N] | mchanges [--mine | ID[..ID] ...] [--summary|--history|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...] | mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]`. Hand reviewers explicit IDs or same-agent inclusive ranges for the requested changes, together with the review scope; bare mchanges and --mine review the calling thread's changes; --list compresses their IDs with status and counts. --net composes selected captured diffs, not a live Git diff. Choose editing tools for the task, not capture support. Use mchanges when its evidence covers the review; use a scoped workspace diff or file inspection for uncovered paths or missing baselines. Changes observed describes captured filesystem differences, not confirmed tool success; --history shows confirmation and capture details. Missing confirmation alone does not require repeating an edit or review. Avoid duplicate reviews of the same evidence; skip --summary before an already-needed diff. `revert` undoes and `apply` replays selected changes in the workspace, git-style: drifted regions merge; overlapping edits leave conflict markers (exit 1). Output states each file relative to mchanges history, not Git (`clean`, `+N -N`, `UU` conflict, `??` unknown). The revert is itself recorded as a change; follow the printed undo line. Recorded diffs are historical evidence, not proof of current workspace contents.",
    },
    nativeExecutor: "mchanges",
  };
}
