<script lang="ts">
  import type { Snippet } from "svelte";
  import * as InputGroup from "./input-group";

  // Prompt Input's shared frame; draft, attachment and submission ownership
  // stays with the composer rather than a separate chat transport.
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
  data-slot="prompt-input"
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
  <InputGroup.Root
    class={[
      "h-auto flex-col items-stretch rounded-composer",
      dragging
        ? "border-dashed border-accent bg-accent-tint"
        : invalid
          ? "border-negative"
          : "focus-within:border-muted",
    ]}
  >
    {@render children()}
  </InputGroup.Root>
</form>
