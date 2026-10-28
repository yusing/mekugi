<script lang="ts">
  import type { Snippet } from "svelte";
  import Button from "./Button.svelte";
  import * as DialogPrimitive from "./dialog";

  // Modal on desktop, a safe-area-aware sheet on phones. Form fields take
  // initial focus; a dialog without a field focuses its unringed container.
  let {
    title,
    children,
    onclose,
    wide = false,
  }: {
    title: string;
    children: Snippet;
    onclose: () => void;
    wide?: boolean;
  } = $props();
  let content = $state<HTMLDivElement | null>(null);
</script>

<DialogPrimitive.Root
  open
  onOpenChange={(open) => {
    if (!open) onclose();
  }}
>
  <DialogPrimitive.Content
    bind:ref={content}
    showCloseButton={false}
    aria-describedby={undefined}
    class={wide ? "w-[min(720px,calc(100vw-24px))]" : "w-[min(520px,calc(100vw-24px))]"}
    onInteractOutside={(event) => event.preventDefault()}
    onOpenAutoFocus={(event) => {
      event.preventDefault();
      (
        content?.querySelector<HTMLElement>("input:not([type=hidden]), textarea, select") ?? content
      )?.focus();
    }}
  >
    <header class="-mt-1 -mr-1.5 mb-4 flex items-center justify-between gap-4">
      <DialogPrimitive.Title class="text-base leading-tight font-medium"
        >{title}</DialogPrimitive.Title
      >
      <DialogPrimitive.Close>
        {#snippet child({ props })}
          <Button variant="quiet" size="sm" icon="close" label="Close dialog" {...props} />
        {/snippet}
      </DialogPrimitive.Close>
    </header>
    {@render children()}
  </DialogPrimitive.Content>
</DialogPrimitive.Root>
