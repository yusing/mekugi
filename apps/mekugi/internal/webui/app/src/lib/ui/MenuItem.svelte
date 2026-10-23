<script lang="ts">
  import type { Snippet } from "svelte";
  import Icon, { type IconName } from "./Icon.svelte";

  // One action in a Menu.
  let {
    icon,
    disabled = false,
    onclick,
    children,
  }: { icon?: IconName; disabled?: boolean; onclick: () => void; children: Snippet } = $props();
</script>

<button
  type="button"
  role="menuitem"
  class="flex min-h-[34px] w-full items-center gap-2.5 rounded-control px-2.5 py-1.5 text-left text-ink hover:enabled:bg-active focus-visible:bg-active focus-visible:outline-none disabled:text-muted max-sm:min-h-11"
  {disabled}
  {onclick}
>
  {#if icon}<span class="flex flex-none text-muted"><Icon name={icon} /></span>{/if}
  {@render children()}
</button>
