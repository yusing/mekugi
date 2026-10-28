<script lang="ts">
  import type { Snippet } from "svelte";
  import Button from "./Button.svelte";
  import type { IconName } from "./Icon.svelte";
  import * as PopoverPrimitive from "./popover";

  // Expensive contents mount once and stay cached while the panel is closed.
  let {
    label,
    icon,
    tooltip = false,
    children,
  }: {
    label: string;
    icon: IconName;
    tooltip?: boolean;
    children: Snippet<[() => void]>;
  } = $props();
  let open = $state(false);
  let mounted = $state(false);
</script>

<PopoverPrimitive.Root
  bind:open
  onOpenChange={(next) => {
    if (next) mounted = true;
  }}
>
  <PopoverPrimitive.Trigger>
    {#snippet child({ props })}
      <Button variant="quiet" {icon} {label} {tooltip} {...props} />
    {/snippet}
  </PopoverPrimitive.Trigger>
  {#if mounted}
    <PopoverPrimitive.Content
      forceMount
      align="end"
      sideOffset={6}
      collisionPadding={12}
      aria-label={label}
      style={open ? undefined : "display: none"}
      class="max-h-[min(calc(100dvh-var(--topbar)-24px),var(--bits-popover-content-available-height))] w-[min(360px,calc(100vw-24px))] overflow-y-auto p-0"
    >
      {@render children(() => (open = false))}
    </PopoverPrimitive.Content>
  {/if}
</PopoverPrimitive.Root>
