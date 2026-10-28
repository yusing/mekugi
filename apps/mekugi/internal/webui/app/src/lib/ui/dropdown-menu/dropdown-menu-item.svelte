<script lang="ts">
  import { DropdownMenu as DropdownMenuPrimitive } from "bits-ui";
  import { cn } from "$lib/utils.js";

  let {
    ref = $bindable(null),
    class: className,
    inset,
    variant = "default",
    ...restProps
  }: DropdownMenuPrimitive.ItemProps & {
    inset?: boolean;
    variant?: "default" | "destructive";
  } = $props();
</script>

<DropdownMenuPrimitive.Item
  bind:ref
  data-slot="dropdown-menu-item"
  data-inset={inset}
  data-variant={variant}
  class={cn(
    "relative flex min-h-[34px] w-full cursor-default items-center gap-2.5 rounded-control px-2.5 py-1.5 text-left text-ink outline-none select-none data-highlighted:bg-active data-disabled:pointer-events-none data-disabled:text-muted data-[variant=destructive]:text-negative data-inset:pl-7 max-sm:min-h-11",
    className,
  )}
  {...restProps}
/>
