<script lang="ts">
  import type { Snippet } from "svelte";
  import Button from "./Button.svelte";

  // A modal dialog, centred on wide screens and a sheet from the foot of a
  // phone's screen, clear of the home indicator. Opening it may focus the
  // dialog itself, a container rather than a control, so it is never ringed.
  let {
    title,
    children,
    onclose,
    wide = false,
  }: { title: string; children: Snippet; onclose: () => void; wide?: boolean } = $props();
  let dialog: HTMLDialogElement;
  $effect(() => {
    const opener = document.activeElement;
    dialog.showModal();
    dialog.querySelector<HTMLElement>("input:not([type=hidden]), textarea, select")?.focus();
    return () => {
      dialog.close();
      queueMicrotask(() => {
        if (opener instanceof HTMLElement && opener.isConnected)
          opener.focus({ preventScroll: true });
      });
    };
  });
</script>

<dialog
  bind:this={dialog}
  class={[
    "m-auto max-h-[calc(100dvh-32px)] rounded-sheet border outline-none bg-surface px-5 pt-4 pb-5 text-ink shadow-float wrap-anywhere backdrop:bg-scrim",
    wide ? "w-[min(720px,calc(100vw-24px))]" : "w-[min(520px,calc(100vw-24px))]",
    "max-sm:mx-0 max-sm:mb-0 max-sm:max-h-[92dvh] max-sm:w-screen max-sm:max-w-none max-sm:rounded-b-none max-sm:pb-safe-5",
  ]}
  aria-label={title}
  oncancel={(event) => {
    event.preventDefault();
    onclose();
  }}
>
  <header class="-mr-2 mb-3 flex items-center justify-between gap-4">
    <h2 class="text-base font-semibold">{title}</h2>
    <Button variant="quiet" icon="close" label="Close dialog" onclick={onclose} />
  </header>
  {@render children()}
</dialog>
