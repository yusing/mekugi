<script lang="ts">
  import type { Snippet } from "svelte";
  import { Item } from "./dropdown-menu";
  import Icon, { type IconName } from "./Icon.svelte";

  // One action in a Menu.
  let {
    icon,
    disabled = false,
    onclick,
    children,
  }: { icon?: IconName; disabled?: boolean; onclick: () => void; children: Snippet } = $props();
</script>

<Item {disabled} onSelect={onclick}>
  {#if icon}<span class="flex flex-none text-muted"><Icon name={icon} /></span>{/if}
  {@render children()}
</Item>
