<script lang="ts" module>
  export type Choice = { value: string; label: string; detail?: string };
  export type ChoiceGroup = { label?: string; choices: Choice[] };
</script>

<script lang="ts">
  import { tick } from "svelte";
  import { newIdentity } from "../transport";
  import Icon, { type IconName } from "./Icon.svelte";
  import { beside } from "./place";
  import { chip, floating } from "./styles";

  // A toolbar chip showing the chosen value and the native popover listbox it
  // opens beside itself. The browser handles light dismissal, Escape and focus
  // return; arrow keys move between choices. With entry, a field above the
  // list also takes an unlisted value, filtering the list as it is typed.
  let {
    label,
    value = $bindable(),
    groups,
    icon,
    text,
    title = label,
    entry,
    disabled = false,
    class: extra = "",
    onchange,
  }: {
    label: string;
    value: string;
    groups: ChoiceGroup[];
    icon?: IconName;
    text?: string;
    title?: string;
    entry?: string;
    disabled?: boolean;
    class?: string;
    onchange?: (value: string) => void;
  } = $props();

  const id = `picker-${newIdentity()}`;
  let button = $state<HTMLButtonElement>();
  let panel = $state<HTMLElement>();
  let field = $state<HTMLInputElement>();
  let open = $state(false);
  let place = $state("");
  // What the field holds; the list filters only once it is edited.
  let draft = $state("");
  let query = $state<string>();
  const shown = $derived.by(() => {
    const needle = query?.trim().toLowerCase() ?? "";
    return groups
      .map((group) => ({
        ...group,
        choices: group.choices.filter((choice) =>
          [choice.value, choice.label, choice.detail].some((part) =>
            part?.toLowerCase().includes(needle),
          ),
        ),
      }))
      .filter((group) => group.choices.length);
  });
  const current = $derived(
    groups.flatMap((group) => group.choices).find((choice) => choice.value === value),
  );

  function choose(next: string) {
    panel?.hidePopover();
    if (next === value) return;
    value = next;
    onchange?.(next);
  }
  function options(): HTMLElement[] {
    return Array.from(panel?.querySelectorAll<HTMLElement>("[role=option]") ?? []);
  }
  // The popover is at least as wide as its chip. It is placed before it
  // shows, then again once its width is known.
  function position(width: number) {
    if (button) place = `min-width:${button.offsetWidth}px;${beside(button, width)}`;
  }
  async function onbeforetoggle(event: ToggleEvent) {
    open = event.newState === "open";
    if (!open) return;
    draft = value;
    query = undefined;
    position(360);
    await tick();
    if (!open || !panel) return;
    position(panel.offsetWidth);
    if (field) field.select();
    else
      (
        options().find((option) => option.getAttribute("aria-selected") === "true") ?? options()[0]
      )?.focus();
  }
  function onkeydown(event: KeyboardEvent) {
    const all = options();
    const at = all.indexOf(document.activeElement as HTMLElement);
    let next: number;
    if (event.key === "ArrowDown") next = at + 1;
    else if (event.key === "ArrowUp") next = at - 1;
    else if (event.key === "Home" && at >= 0) next = 0;
    else if (event.key === "End" && at >= 0) next = all.length - 1;
    else return;
    event.preventDefault();
    // Above the first choice is the field, when there is one.
    if (next < 0 && field) field.focus();
    else if (all.length) all[(next + all.length) % all.length].focus();
  }
</script>

<button
  bind:this={button}
  type="button"
  class="{chip} {extra}"
  {title}
  {disabled}
  popovertarget={id}
  aria-haspopup="listbox"
  aria-expanded={open}
>
  {#if icon}<span class="flex flex-none"><Icon name={icon} size={14} /></span>{/if}
  <span class="sr-only">{label}:</span>
  <span class="min-w-0 flex-1 truncate text-left text-ink">{text ?? current?.label ?? value}</span>
  <span class="flex flex-none"><Icon name="chevron" size={14} /></span>
</button>
<!-- svelte-ignore a11y_no_static_element_interactions (keys move focus among the choices) -->
<div
  bind:this={panel}
  {id}
  class="fixed inset-auto m-0 hidden max-h-[min(440px,var(--room))] w-max max-w-[min(360px,calc(100vw-16px))] flex-col p-1 open:flex {floating}"
  style={place}
  popover="auto"
  onbeforetoggle={(event) => void onbeforetoggle(event as ToggleEvent)}
  {onkeydown}
>
  {#if entry}
    <input
      bind:this={field}
      class="mb-1 min-h-8 w-full min-w-0 flex-none rounded-control border border-control bg-surface px-2.5 font-mono text-small text-ink max-sm:min-h-11 max-sm:text-base"
      aria-label={entry}
      placeholder={entry}
      autocomplete="off"
      spellcheck="false"
      bind:value={draft}
      oninput={() => (query = draft)}
      onkeydown={(event) => {
        // The picker sits in a form; Enter takes the typed value instead.
        if (event.key !== "Enter" || event.isComposing) return;
        event.preventDefault();
        if (draft.trim()) choose(draft.trim());
      }}
    />
  {/if}
  <div class="min-h-0 overflow-y-auto" role="listbox" aria-label={label}>
    {#each shown as group, index (group.label ?? index)}
      <div role="group" aria-label={group.label}>
        {#if group.label}<div
            class="px-2.5 pt-2 pb-1 text-tiny font-medium text-muted"
            aria-hidden="true"
          >
            {group.label}
          </div>{/if}
        {#each group.choices as choice (choice.value)}
          <button
            type="button"
            role="option"
            tabindex="-1"
            aria-selected={choice.value === value}
            class="grid min-h-[34px] w-full grid-cols-[16px_minmax(0,1fr)] items-center gap-x-2 rounded-control py-1.5 pr-3 pl-2 text-left text-ink hover:bg-active focus-visible:bg-active focus-visible:outline-none max-sm:min-h-11"
            onclick={() => choose(choice.value)}
          >
            <span class="flex text-muted"
              >{#if choice.value === value}<Icon name="check" />{/if}</span
            >
            <span class="truncate">{choice.label}</span>
            {#if choice.detail}<span class="col-start-2 truncate text-small text-muted"
                >{choice.detail}</span
              >{/if}
          </button>
        {/each}
      </div>
    {:else}
      <p class="px-2.5 py-1.5 text-small text-muted">
        {!entry
          ? "Nothing to choose."
          : query?.trim()
            ? "Press Enter to use what you typed."
            : "Nothing listed; type a value."}
      </p>
    {/each}
  </div>
</div>
