<script lang="ts">
  import { untrack } from "svelte";
  import type { NativeEffect } from "../../../../sessionapi/protocol";
  import { semantic } from "./semantic.svelte";
  import Button from "./ui/Button.svelte";

  let { effect, disabled }: { effect: NativeEffect; disabled: boolean } = $props();
  const parts = $derived(effect.Recovery?.parts ?? []);
  let text = $state(untrack(() => parts.filter((_, i) => i % 2 === 0)));
</script>

<div class="grid gap-2 py-2">
  {#each parts as part, i (i)}
    {#if i % 2 === 0}
      <textarea
        class="min-h-11 w-full resize-y rounded-control border border-control bg-surface px-3 py-2 text-ink field-sizing-content"
        aria-label={parts.length === 1 ? "Edit queued message" : `Message text ${i / 2 + 1}`}
        bind:value={text[i / 2]}
        {disabled}
        rows="1"></textarea>
    {:else}
      <span class="w-fit max-w-full rounded-control bg-active px-2 py-1 text-small wrap-anywhere"
        >{part}</span
      >
    {/if}
  {/each}
  {#if effect.Recovery?.attachments}<p class="text-small text-muted">
      Original attachments are retained.
    </p>{/if}
  <div class="flex justify-end gap-2">
    <Button
      variant="quiet"
      {disabled}
      onclick={() => semantic.send({ recovery: { effect: effect.ID, action: "discard" } })}
      >Discard</Button
    >
    <Button
      disabled={disabled || effect.Thread !== semantic.frame?.view.Input.ID}
      onclick={() => semantic.send({ recovery: { effect: effect.ID, action: "retry", text } })}
      >Send</Button
    >
  </div>
</div>
