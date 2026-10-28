<script lang="ts">
  import { Select as SelectPrimitive } from "bits-ui";
  import CheckIcon from "@lucide/svelte/icons/check";
  import { cn, type WithoutChild } from "$lib/utils.js";

  let {
    ref = $bindable(null),
    class: className,
    value,
    label,
    children: childrenProp,
    ...restProps
  }: WithoutChild<SelectPrimitive.ItemProps> = $props();
</script>

<SelectPrimitive.Item
  bind:ref
  {value}
  {label}
  data-slot="select-item"
  class={cn(
    "relative grid min-h-[34px] w-full cursor-default grid-cols-[16px_minmax(0,1fr)] items-center gap-x-2 rounded-control py-1.5 pr-3 pl-2 text-left text-ink outline-none select-none data-highlighted:bg-active data-disabled:pointer-events-none data-disabled:text-muted max-sm:min-h-11",
    className,
  )}
  {...restProps}
>
  {#snippet children({ selected, highlighted })}
    <span class="pointer-events-none flex size-4 items-center justify-center text-muted">
      {#if selected}
        <CheckIcon class="pointer-events-none size-4" />
      {/if}
    </span>
    <span class="min-w-0">
      {#if childrenProp}
        {@render childrenProp({ selected, highlighted })}
      {:else}
        {label || value}
      {/if}
    </span>
  {/snippet}
</SelectPrimitive.Item>
