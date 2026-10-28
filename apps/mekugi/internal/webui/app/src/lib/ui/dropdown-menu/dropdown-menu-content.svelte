<script lang="ts">
  import { DropdownMenu as DropdownMenuPrimitive } from "bits-ui";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils.js";
  import DropdownMenuPortal from "./dropdown-menu-portal.svelte";
  import type { ComponentProps } from "svelte";

  let {
    ref = $bindable(null),
    sideOffset = 4,
    align = "start",
    portalProps,
    class: className,
    ...restProps
  }: DropdownMenuPrimitive.ContentProps & {
    portalProps?: WithoutChildrenOrChild<ComponentProps<typeof DropdownMenuPortal>>;
  } = $props();
</script>

<DropdownMenuPortal {...portalProps}>
  <DropdownMenuPrimitive.Content
    bind:ref
    data-slot="dropdown-menu-content"
    {sideOffset}
    {align}
    class={cn(
      "z-50 w-max min-w-50 max-w-[min(320px,calc(100vw-16px))] max-h-[min(420px,var(--bits-dropdown-menu-content-available-height))] overflow-y-auto rounded-float border border-border bg-surface p-1 text-ink shadow-float outline-none",
      className,
    )}
    {...restProps}
  />
</DropdownMenuPortal>
