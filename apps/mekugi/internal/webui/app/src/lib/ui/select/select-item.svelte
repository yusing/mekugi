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
    "focus:bg-ui-accent focus:text-accent-foreground not-data-[variant=destructive]:focus:**:text-accent-foreground gap-1.5 rounded-md py-1 pr-8 pl-1.5 text-sm [&_svg:not([class*='size-'])]:size-4 relative flex w-full cursor-default items-center text-left outline-hidden select-none data-disabled:pointer-events-none data-disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 data-highlighted:bg-ui-accent data-highlighted:text-accent-foreground max-sm:min-h-11",
    className,
  )}
  {...restProps}
>
  {#snippet children({ selected, highlighted })}
    <span class="pointer-events-none absolute right-2 flex size-4 items-center justify-center">
      {#if selected}
        <CheckIcon class="pointer-events-none size-4" />
      {/if}
    </span>
    <span class="min-w-0 flex-1">
      {#if childrenProp}
        {@render childrenProp({ selected, highlighted })}
      {:else}
        {label || value}
      {/if}
    </span>
  {/snippet}
</SelectPrimitive.Item>
