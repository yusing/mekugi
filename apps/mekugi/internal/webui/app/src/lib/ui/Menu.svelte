<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import { newIdentity } from "../transport";
  import Button from "./Button.svelte";
  import type { IconName } from "./Icon.svelte";
  import { beside } from "./place";
  import { floating } from "./styles";

  // An icon button and the native popover menu it opens beside itself, above
  // the button when there is more room there. The browser handles light
  // dismissal, Escape and focus return; arrow keys move between items.
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

  const id = `menu-${newIdentity()}`;
  let button = $state<HTMLButtonElement>();
  let panel = $state<HTMLElement>();
  let open = $state(false);
  let place = $state("");

  function close() {
    panel?.hidePopover();
  }
  function items(): HTMLElement[] {
    return Array.from(panel?.querySelectorAll<HTMLElement>("[role=menuitem]:not(:disabled)") ?? []);
  }
  async function onbeforetoggle(event: ToggleEvent) {
    open = event.newState === "open";
    if (!open || !button) return;
    place = beside(button, 280);
    await tick();
    if (open && panel) {
      place = beside(button, panel.offsetWidth);
      items()[0]?.focus();
    }
  }
  function onkeydown(event: KeyboardEvent) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const all = items();
    if (!all.length) return;
    event.preventDefault();
    const at = all.indexOf(document.activeElement as HTMLElement);
    const step = event.key === "ArrowDown" ? 1 : -1;
    all[(at + step + all.length) % all.length].focus();
  }
</script>

<Button
  bind:ref={button}
  variant="quiet"
  {icon}
  {label}
  {disabled}
  popovertarget={id}
  aria-haspopup="menu"
  aria-expanded={open}
/>
<!-- svelte-ignore a11y_no_noninteractive_element_interactions (the menu moves focus among its items) -->
<div
  bind:this={panel}
  {id}
  class="fixed inset-auto m-0 max-h-[min(420px,var(--room))] w-max max-w-[min(320px,calc(100vw-16px))] min-w-50 overflow-y-auto p-1 {floating}"
  role="menu"
  tabindex="-1"
  aria-label={label}
  popover="auto"
  style={place}
  onbeforetoggle={(event) => void onbeforetoggle(event as ToggleEvent)}
  {onkeydown}
>
  {@render children(close)}
</div>
