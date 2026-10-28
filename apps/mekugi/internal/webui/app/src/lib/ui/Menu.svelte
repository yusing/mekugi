<script lang="ts">
  import type { Snippet } from "svelte";
  import Button from "./Button.svelte";
  import type { IconName } from "./Icon.svelte";
  import * as DropdownMenu from "./dropdown-menu";

  let {
    label,
    icon,
    children,
    disabled = false,
  }: {
    label: string;
    icon: IconName;
    children: Snippet<[() => void]>;
    disabled?: boolean;
  } = $props();
  let open = $state(false);
</script>

<DropdownMenu.Root bind:open>
  <DropdownMenu.Trigger {disabled}>
    {#snippet child({ props })}
      <Button variant="quiet" {icon} {label} {...props} />
    {/snippet}
  </DropdownMenu.Trigger>
  <DropdownMenu.Content sideOffset={6} collisionPadding={8} loop aria-label={label}>
    {@render children(() => (open = false))}
  </DropdownMenu.Content>
</DropdownMenu.Root>
