<script lang="ts" module>
  export type Choice = { value: string; label: string; detail?: string };
  export type ChoiceGroup = { label?: string; choices: Choice[] };
</script>

<script lang="ts">
  import { Combobox } from "bits-ui";
  import ChevronDownIcon from "@lucide/svelte/icons/chevron-down";
  import { cn } from "$lib/utils.js";
  import Icon, { type IconName } from "./Icon.svelte";
  import * as Select from "./select";
  import * as Popover from "./popover";
  import { Input } from "./input";
  import { selectContent, selectTrigger } from "./select/styles";

  // Select owns choice navigation. Free-entry pickers use its matching
  // Combobox primitive, with Enter accepting typed text until an arrow key
  // explicitly navigates the available choices.
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

  let field = $state<HTMLInputElement | null>(null);
  let open = $state(false);
  let draft = $state("");
  let query = $state<string>();
  let navigating = $state(false);
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
  const items = $derived(groups.flatMap((group) => group.choices));
  const current = $derived(items.find((choice) => choice.value === value));

  function choose(next: string) {
    open = false;
    if (next === value) return;
    value = next;
    onchange?.(next);
  }

  function opened(next: boolean) {
    if (!next) return;
    draft = value;
    query = undefined;
    navigating = false;
  }
</script>

{#snippet triggerLabel()}
  {#if icon}<span class="flex flex-none"><Icon name={icon} size={14} /></span>{/if}
  <span class="sr-only">{label}:</span>
  <span class="min-w-0 flex-1 truncate text-left text-ink">{text ?? current?.label ?? value}</span>
{/snippet}

{#snippet choices()}
  {#each shown as group, index (group.label ?? index)}
    <Select.Group>
      {#if group.label}<Select.GroupHeading>{group.label}</Select.GroupHeading>{/if}
      {#each group.choices as choice (choice.value)}
        <Select.Item value={choice.value} label={choice.label}>
          <span class="block truncate">{choice.label}</span>
          {#if choice.detail}<span class="block truncate text-small text-muted"
              >{choice.detail}</span
            >{/if}
        </Select.Item>
      {/each}
    </Select.Group>
  {:else}
    <p class="px-2.5 py-1.5 text-small text-muted">
      {!entry
        ? "Nothing to choose."
        : query?.trim()
          ? "Press Enter to use what you typed."
          : "Nothing listed; type a value."}
    </p>
  {/each}
{/snippet}

{#if entry}
  <Popover.Root bind:open onOpenChange={opened}>
    <Popover.Trigger class={cn(selectTrigger, extra)} data-size="default" {title} {disabled}>
      {@render triggerLabel()}
      <ChevronDownIcon class="text-muted-foreground size-4 pointer-events-none" />
    </Popover.Trigger>
    <Popover.Content
      sideOffset={6}
      align="start"
      collisionPadding={8}
      aria-label={label}
      class={cn(
        selectContent,
        "flex min-w-[min(var(--bits-popover-anchor-width),360px,calc(100vw-16px))] max-h-[min(440px,var(--bits-popover-content-available-height))] origin-(--bits-popover-content-transform-origin) flex-col border-0 p-0",
      )}
      onOpenAutoFocus={(event) => {
        event.preventDefault();
        field?.select();
      }}
    >
      <Combobox.Root
        type="single"
        {value}
        {items}
        {disabled}
        allowDeselect={false}
        loop
        bind:open
        inputValue={draft}
        onValueChange={choose}
      >
        <Combobox.Input
          bind:ref={field}
          aria-label={entry}
          placeholder={entry}
          autocomplete="off"
          spellcheck="false"
          oninput={(event) => {
            draft = event.currentTarget.value;
            query = draft;
            navigating = false;
          }}
          onkeydown={(event) => {
            if (event.key === "ArrowDown" || event.key === "ArrowUp") navigating = shown.length > 0;
            if (event.key !== "Enter" || event.isComposing || navigating) return;
            event.preventDefault();
            if (draft.trim()) choose(draft.trim());
          }}
        >
          {#snippet child({ props })}
            <Input class="m-1 w-[calc(100%-0.5rem)] font-mono text-small" {...props} />
          {/snippet}
        </Combobox.Input>
        <Combobox.ContentStatic
          aria-label={label}
          class="min-h-0"
          preventScroll={false}
          onInteractOutside={(event) => event.preventDefault()}
        >
          <Combobox.Viewport class="min-h-0 overflow-y-auto">
            {@render choices()}
          </Combobox.Viewport>
        </Combobox.ContentStatic>
      </Combobox.Root>
    </Popover.Content>
  </Popover.Root>
{:else}
  <Select.Root
    type="single"
    {value}
    {items}
    {disabled}
    allowDeselect={false}
    loop
    bind:open
    onValueChange={choose}
  >
    <Select.Trigger class={extra} {title}>
      {@render triggerLabel()}
    </Select.Trigger>
    <Select.Content sideOffset={6} collisionPadding={8} aria-label={label}>
      {@render choices()}
    </Select.Content>
  </Select.Root>
{/if}
