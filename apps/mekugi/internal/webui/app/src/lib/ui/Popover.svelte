<script lang="ts">
  import type { Snippet } from "svelte";
  import { newIdentity } from "../transport";
  import Button from "./Button.svelte";
  import type { IconName } from "./Icon.svelte";
  import { floating } from "./styles";

  // A top-bar icon button and the native popover it opens under the bar. The
  // browser handles light dismissal, Escape and focus return. The content
  // mounts on first open, stays cached, and can close the panel.
  let {
    label,
    icon,
    children,
  }: { label: string; icon: IconName; children: Snippet<[() => void]> } = $props();

  const id = `popover-${newIdentity()}`;
  let panel = $state<HTMLElement>();
  let open = $state(false);
  let mounted = $state(false);
</script>

<Button variant="quiet" {icon} {label} popovertarget={id} aria-expanded={open} />
<div
  bind:this={panel}
  {id}
  class="fixed inset-auto top-[calc(var(--topbar)+env(safe-area-inset-top,0px)+6px)] right-2 m-0 max-h-[calc(100dvh-var(--topbar)-24px)] w-[min(360px,calc(100vw-24px))] overflow-y-auto p-0 {floating}"
  popover="auto"
  onbeforetoggle={(e) => {
    open = e.newState === "open";
    if (open) mounted = true;
  }}
>
  {#if mounted}{@render children(() => panel?.hidePopover())}{/if}
</div>
