import type {NativeTool} from "../internal/router/toolplugin/plugin.d.ts";

export function createMChangesTool(): NativeTool {
  return {
    specification: {
      type: "custom",
      name: "mchanges",
      description: "Review completed observed stock apply_patch evidence, not a Git diff or provisional preview. Usage: `mchanges --list` or `mchanges ID[..ID] ... [--summary|--history] [-- PATH ...]`. Use Git for shell-generated or unrelated changes; omitted review output supplies mread continuation.",
    },
    nativeExecutor: "mchanges",
  };
}
