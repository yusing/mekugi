<script lang="ts">
  import { Select as SelectPrimitive } from "bits-ui";
  import { cn, type WithoutChild } from "$lib/utils.js";
  import type { WithoutChildrenOrChild } from "$lib/utils.js";
  import SelectPortal from "./select-portal.svelte";
  import SelectScrollDownButton from "./select-scroll-down-button.svelte";
  import SelectScrollUpButton from "./select-scroll-up-button.svelte";
  import type { ComponentProps } from "svelte";
  import { selectContent } from "./styles";

  let {
    ref = $bindable(null),
    class: className,
    sideOffset = 4,
    portalProps,
    children,
    preventScroll = true,
    ...restProps
  }: WithoutChild<SelectPrimitive.ContentProps> & {
    portalProps?: WithoutChildrenOrChild<ComponentProps<typeof SelectPortal>>;
  } = $props();
</script>

<SelectPortal {...portalProps}>
  <SelectPrimitive.Content
    bind:ref
    {sideOffset}
    {preventScroll}
    data-slot="select-content"
    class={cn(
      selectContent,
      "min-w-[min(var(--bits-select-anchor-width),360px,calc(100vw-16px))] max-h-[min(440px,var(--bits-select-content-available-height))] origin-(--bits-select-content-transform-origin)",
      className,
    )}
    {...restProps}
  >
    <SelectScrollUpButton />
    <SelectPrimitive.Viewport class="w-full min-w-0 scroll-my-1">
      {@render children?.()}
    </SelectPrimitive.Viewport>
    <SelectScrollDownButton />
  </SelectPrimitive.Content>
</SelectPortal>
