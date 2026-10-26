<script lang="ts">
  import { diffRows, diffWords, diffTokens, workspacePath } from "./diff";
  import { diffSyntax, type SyntaxToken } from "./highlight";
  import type { ReviewFile } from "./protocol";

  // One captured or previewed file change: what kind of change it is, then
  // its unified diff with line numbers on both sides, without hunk headers. status explains a
  // composition gap; recent marks a change the reader has not seen yet.
  let {
    file,
    workspace,
    status,
    recent = false,
  }: { file: ReviewFile; workspace: string; status?: string; recent?: boolean } = $props();

  const source = $derived(file.Diff ?? "");
  const beforePath = $derived(file.BeforePath);
  const afterPath = $derived(file.AfterPath);
  const rows = $derived(diffRows(source));
  const words = $derived(diffWords(rows));
  let syntax = $state.raw(new Map<number, SyntaxToken[]>());
  $effect(() => {
    const current = rows;
    let active = true;
    syntax = new Map();
    void diffSyntax(current, beforePath, afterPath).then((result) => {
      if (active) syntax = result;
    });
    return () => {
      active = false;
    };
  });
  const digits = $derived(
    String(rows.reduce((n, row) => Math.max(n, row.old ?? 0, row.new ?? 0), 0)).length,
  );
  const path = (p: string) => workspacePath(p, workspace);
  const notes = $derived.by(() => {
    const out: string[] = [];
    if (status) out.push(status);
    else if (file.Incomplete) out.push(file.Incomplete);
    if (file.Directory) out.push("Directory change; its contents are not kept.");
    if (file.Binary) out.push("Binary content changed.");
    if (file.Link) out.push("Symbolic link; the lines are its target.");
    if (file.CopyFrom) out.push(`Copied from ${path(file.CopyFrom)}.`);
    if (!file.BeforePath && file.AfterPath) out.push("Created.");
    else if (file.BeforePath && !file.AfterPath) out.push("Deleted.");
    else if (file.BeforePath && file.BeforePath !== file.AfterPath)
      out.push(`Moved from ${path(file.BeforePath)}.`);
    return out;
  });
  const tints: Record<string, string> = {
    add: "bg-[color-mix(in_srgb,var(--positive)_13%,transparent)]",
    remove: "bg-[color-mix(in_srgb,var(--negative)_13%,transparent)]",
  };
  const signs: Record<string, string> = {
    add: "text-positive",
    remove: "text-negative",
  };
  const number = "flex-none pr-2 text-right text-muted tabular-nums select-none";
</script>

<div class={["mt-1.5", recent && "border-l-2 border-l-accent pl-2"]}>
  {#each notes as note, i (i)}<p class="mb-1 text-small text-muted">{note}</p>{/each}
  {#if rows.length}
    <div class="overflow-x-auto overscroll-x-contain rounded-card border font-mono text-code">
      <div class="min-w-max py-1">
        {#each rows as row, i (i)}
          {#if row.kind === "hunk"}
            <!-- Line numbers place each hunk; a rule marks the lines between. -->
            <div class="my-1 border-t border-dashed border-rule first:hidden"></div>
          {:else if row.kind === "note"}
            <div class="px-2.5 text-muted italic">{row.text}</div>
          {:else}
            <div class="flex {tints[row.kind] ?? ''}" class:bg-add={row.kind === "add"}>
              <span class={number} style:width="{digits + 2}ch">{row.old ?? ""}</span>
              <span class={number} style:width="{digits + 2}ch">{row.new ?? ""}</span>
              <span class="w-[2ch] flex-none text-center select-none {signs[row.kind] ?? ''}"
                >{row.kind === "add" ? "+" : row.kind === "remove" ? "−" : ""}</span
              >
              <span class="pr-3 whitespace-pre"
                >{#each diffTokens(row.text, syntax.get(i), words.get(i)) as token, j (j)}<span
                    style={token.style}
                    class:diff-word={token.changed}>{token.text}</span
                  >{/each}</span
              >
            </div>
          {/if}
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  /* Tinted rows need stronger ink than the themes' white/black code surfaces.
     Keep each syntax hue while giving comments and changed words enough contrast. */
  [style*="--shiki-light"] {
    color: color-mix(in srgb, var(--shiki-light) 60%, var(--text));
  }
  @media (prefers-color-scheme: dark) {
    [style*="--shiki-dark"] {
      color: color-mix(in srgb, var(--shiki-dark) 60%, var(--text));
    }
  }
  .diff-word {
    background: color-mix(in srgb, var(--negative) 16%, transparent);
    border-radius: 2px;
  }
  .bg-add .diff-word {
    background: color-mix(in srgb, var(--positive) 16%, transparent);
  }
</style>
