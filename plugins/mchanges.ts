import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";

export function createMChangesTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mchanges",
      description: "Review, revert, or reapply completed observed evidence from stock apply_patch and declared shell file operations such as redirects, cp, mv, rm, and sed -i; not a Git diff or provisional preview. Usage: `mchanges --list`, `mchanges ID[..ID] ... [--summary|--history] [-- PATH ...]`, or `mchanges revert|apply ID[..ID] ... [-- PATH ...]`. Hand reviewers explicit IDs or same-agent inclusive ranges for the requested changes, together with the review scope; --list only lists the calling thread's changes. Prefer mchanges for captured edits. Use Git for other shell-generated or unrelated changes. Do not routinely pair Git diff with mchanges for the same edits; skip --summary before an already-needed diff. `revert` undoes and `apply` replays selected changes in the workspace, git-style: drifted regions merge; overlapping edits leave conflict markers (exit 1). Output states each file relative to mchanges history, not Git (`clean`, `+N -N`, `UU` conflict, `??` unknown). The revert is itself recorded as a change; follow the printed undo line. Omitted review output supplies mread continuation; recorded diffs are historical evidence, not proof of current workspace contents.",
    },
    nativeExecutor: "mchanges",
  };
}
