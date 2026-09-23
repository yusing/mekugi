import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";

export function createMChangesTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mchanges",
      description: "Review completed observed evidence from stock apply_patch and declared shell file operations such as redirects, cp, mv, rm, and sed -i; not a Git diff or provisional preview. Usage: `mchanges --list` or `mchanges ID[..ID] ... [--summary|--history] [-- PATH ...]`. Hand reviewers explicit IDs or same-agent inclusive ranges for the requested changes, together with the review scope; --list only lists the calling thread's changes. Prefer mchanges for captured edits. Use Git for other shell-generated or unrelated changes. Do not routinely pair Git diff with mchanges for the same edits; skip --summary before an already-needed diff. Omitted review output supplies mread continuation; recorded diffs are historical evidence, not proof of current workspace contents.",
    },
    nativeExecutor: "mchanges",
  };
}
