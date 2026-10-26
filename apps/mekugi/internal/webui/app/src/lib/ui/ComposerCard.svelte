<script lang="ts">
  import type { Snippet } from "svelte";

  // The frame of a composer: a card that takes dropped files while accepts
  // allows them, and submits as a form.
  let {
    accepts,
    onfiles,
    onsubmit,
    invalid = false,
    children,
  }: {
    accepts: boolean;
    onfiles: (files: FileList) => void;
    onsubmit: () => void;
    invalid?: boolean;
    children: Snippet;
  } = $props();
  let dragging = $state(false);
  const hasFiles = (event: DragEvent) => event.dataTransfer?.types.includes("Files") ?? false;
</script>

<form
  class={[
    "rounded-composer border transition-colors",
    dragging
      ? "border-dashed border-accent bg-accent-tint"
      : invalid
        ? "border-negative bg-surface"
        : "border-control bg-surface focus-within:border-muted",
  ]}
  onsubmit={(event) => {
    event.preventDefault();
    onsubmit();
  }}
  ondragover={(event) => {
    if (!hasFiles(event) || !accepts) return;
    event.preventDefault();
    dragging = true;
  }}
  ondragleave={(event) => {
    if (!(event.currentTarget as HTMLElement).contains(event.relatedTarget as Node))
      dragging = false;
  }}
  ondrop={(event) => {
    if (!hasFiles(event) || !accepts) return;
    event.preventDefault();
    dragging = false;
    if (event.dataTransfer?.files) onfiles(event.dataTransfer.files);
  }}
>
  {@render children()}
</form>
