<script lang="ts">
  import type { Snippet } from "svelte";
  import Button from "./Button.svelte";
  import * as DialogPrimitive from "./dialog";

  // Modal on desktop, a safe-area-aware sheet on phones. Form fields take
  // initial focus; a dialog without a field focuses its unringed container.
  // nav holds controls that switch the body's view; they stay pinned under
  // the title while the container, still the focused scroller, scrolls it.
  let {
    title,
    children,
    nav,
    onclose,
    wide = false,
  }: {
    title: string;
    children: Snippet;
    nav?: Snippet;
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
    <!-- With nav, the heading is pinned to the container's top edge: sticky
         insets start inside the scroller's padding, so the inset and margins
         cancel it and the background covers what scrolls beneath. -->
    <div class={nav && "sticky -top-4 z-10 -mx-4 -mt-4 bg-popover px-4 pt-4 pb-4"}>
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
      {@render nav?.()}
    </div>
    {@render children()}
  </DialogPrimitive.Content>
</DialogPrimitive.Root>
