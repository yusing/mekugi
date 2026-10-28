<script lang="ts">
  import { cn, type WithElementRef } from "$lib/utils.js";
  import type { HTMLAttributes } from "svelte/elements";

  let {
    ref = $bindable(null),
    class: className,
    children,
    ...restProps
  }: WithElementRef<HTMLAttributes<HTMLTableRowElement>, HTMLTableRowElement> = $props();
</script>

<tr
  bind:this={ref}
  data-slot="table-row"
  class={cn(
    "hover:bg-ui-muted/50 data-[state=selected]:bg-ui-muted border-b transition-colors has-aria-expanded:bg-ui-muted/50",
    className,
  )}
  {...restProps}
>
  {@render children?.()}
</tr>
