<script lang="ts">
  import { tick } from "svelte";
  import { basename, statusText } from "./format";
  import { hub, type Machine } from "./hub.svelte";
  import type { Listed } from "./protocol";
  import { appearance, themes } from "./theme.svelte";
  import Dot from "./ui/Dot.svelte";
  import Icon, { type IconName } from "./ui/Icon.svelte";
  import { floating, kbd } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";
  import { open, select, view } from "./view.svelte";

  // Keyboard-first thread and action switcher (Cmd/Ctrl+K), inspired by
  // Codex Desktop and T3 Code for jumping across machines and threads.
  let {
    onclose,
    onmachines,
    ontogglesidebar,
  }: {
    onclose: () => void;
    onmachines: () => void;
    ontogglesidebar: () => void;
  } = $props();

  let query = $state("");
  let active = $state(0);
  let field = $state<HTMLInputElement>();

  $effect(() => {
    void tick().then(() => field?.focus());
  });

  type Entry = {
    id: string;
    group: "Threads" | "Actions";
    title: string;
    subtitle?: string;
    machine?: string;
    icon?: IconName;
    tone?: "running" | "alert" | "none";
    waiting?: number;
    replay?: boolean;
    current?: boolean;
    run: () => void;
  };

  const threadName = (s: Listed) =>
    s.title || (s.workspace ? basename(s.workspace) : undefined) || s.thread || "New thread";

  function startMachine(machine?: Machine) {
    select(
      machine?.id ??
        view.machine ??
        hub.machines.find((m) => m.connection === "open")?.id ??
        hub.machines[0]?.id,
    );
    onclose();
    queueMicrotask(() => document.getElementById("new-text")?.focus());
  }

  const items = $derived.by((): Entry[] => {
    const q = query.trim().toLowerCase();
    const threads: Entry[] = [];
    for (const machine of hub.machines) {
      for (const s of machine.sessions ?? []) {
        const title = threadName(s);
        const workspace = s.workspace ? basename(s.workspace) : undefined;
        const haystack = [title, s.workspace ?? "", machine.label, s.id, statusText(s.status)]
          .join(" ")
          .toLowerCase();
        if (q && !haystack.includes(q)) continue;
        threads.push({
          id: `thread:${machine.id}:${s.id}`,
          group: "Threads",
          title,
          subtitle: workspace,
          machine: machine.label,
          tone: s.status.alert ? "alert" : s.status.running ? "running" : "none",
          waiting: s.waiting,
          replay: s.replay,
          current: view.machine === machine.id && view.session === s.id,
          run: () => {
            select(machine.id);
            open(s.id);
            onclose();
          },
        });
      }
    }

    const nextTheme = themes[(themes.indexOf(appearance.choice) + 1) % themes.length];
    const allActions: Entry[] = [
      {
        id: "action:new-thread",
        group: "Actions",
        title: "New thread",
        subtitle: "Start a fresh conversation or resume by ID",
        icon: "compose",
        run: () => startMachine(),
      },
      {
        id: "action:machines",
        group: "Actions",
        title: "Manage machines",
        subtitle: `${hub.machines.length} ${hub.machines.length === 1 ? "machine" : "machines"} configured`,
        icon: "server",
        run: () => {
          onclose();
          onmachines();
        },
      },
      {
        id: "action:sidebar",
        group: "Actions",
        title: "Toggle sidebar",
        subtitle: "Show or hide the thread list",
        icon: "sidebar",
        run: () => {
          onclose();
          ontogglesidebar();
        },
      },
      {
        id: "action:theme",
        group: "Actions",
        title: `Switch theme to ${nextTheme[0].toUpperCase() + nextTheme.slice(1)}`,
        subtitle: `Currently ${appearance.choice}`,
        icon: nextTheme === "dark" ? "moon" : nextTheme === "light" ? "sun" : "monitor",
        run: () => {
          appearance.set(nextTheme);
          onclose();
        },
      },
    ];
    const actions = allActions.filter(
      (item) => !q || `${item.title} ${item.subtitle ?? ""}`.toLowerCase().includes(q),
    );

    return [...threads, ...actions];
  });

  $effect(() => {
    void items.length;
    if (active >= items.length) active = Math.max(0, items.length - 1);
  });

  $effect(() => {
    document.getElementById(`quick-item-${active}`)?.scrollIntoView({ block: "nearest" });
  });

  function onkeydown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      onclose();
      return;
    }
    if (!items.length) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      active = (active + step + items.length) % items.length;
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      items[active]?.run();
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
<div
  class="fixed inset-0 z-50 flex items-start justify-center bg-scrim px-4 pt-[12dvh] backdrop-blur-[2px] max-sm:pt-6"
  onclick={(event) => {
    if (event.target === event.currentTarget) onclose();
  }}
>
  <div
    role="dialog"
    aria-modal="true"
    aria-label="Quick switcher"
    class="{floating} menu-enter flex max-h-[min(520px,76dvh)] w-[min(560px,100%)] flex-col overflow-hidden"
  >
    <div class="flex items-center gap-2.5 border-b border-rule px-3.5 py-2.5">
      <span class="flex flex-none text-muted"><Icon name="search" size={16} /></span>
      <input
        bind:this={field}
        bind:value={query}
        {onkeydown}
        type="text"
        role="combobox"
        aria-expanded="true"
        aria-controls="quick-switcher-list"
        aria-activedescendant={items.length ? `quick-item-${active}` : undefined}
        aria-label="Search threads and actions"
        placeholder="Jump to a thread, workspace, or action…"
        class="min-w-0 flex-1 bg-transparent text-body text-ink focus:outline-none max-sm:text-base"
        autocomplete="off"
        spellcheck="false"
      />
      <kbd class="{kbd} max-sm:hidden">Esc</kbd>
    </div>

    <div
      id="quick-switcher-list"
      role="listbox"
      aria-label="Threads and actions"
      class="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1.5"
    >
      {#if items.length}
        {#each items as item, index (item.id)}
          {#if index === 0 || items[index - 1].group !== item.group}
            <div
              class="px-2.5 pt-2 pb-1 text-tiny font-medium tracking-wide text-muted uppercase first:pt-1"
            >
              {item.group}
            </div>
          {/if}
          <button
            id="quick-item-{index}"
            type="button"
            role="option"
            aria-selected={index === active}
            class="flex min-h-10 w-full items-center gap-2.5 rounded-control px-2.5 py-1.5 text-left transition-colors aria-selected:bg-active max-sm:min-h-11"
            onpointerenter={() => (active = index)}
            onclick={item.run}
          >
            {#if item.icon}
              <span class="flex flex-none text-muted"><Icon name={item.icon} size={15} /></span>
            {:else}
              <span class="flex w-4 flex-none items-center justify-center">
                <Dot tone={item.tone ?? "none"} />
              </span>
            {/if}
            <span class="flex min-w-0 flex-1 flex-col">
              <span class="flex min-w-0 items-center gap-2">
                <span class="truncate font-medium text-ink">{item.title}</span>
                {#if item.current}<Tag tone="live">Current</Tag>{/if}
                {#if item.replay}<Tag>Replay</Tag>{/if}
              </span>
              {#if item.subtitle}
                <span class="truncate text-small text-muted">{item.subtitle}</span>
              {/if}
            </span>
            {#if item.waiting}
              <span
                class="min-w-[18px] flex-none rounded-full bg-warning-tint px-[5px] text-center text-tiny text-warning-ink tabular-nums ring-1 ring-warning-edge ring-inset"
                >{item.waiting}</span
              >
            {/if}
            {#if item.machine}
              <span class="flex-none text-tiny text-muted">{item.machine}</span>
            {/if}
          </button>
        {/each}
      {:else}
        <p class="px-3 py-6 text-center text-small text-muted">No matching threads or actions.</p>
      {/if}
    </div>

    <div
      class="flex items-center justify-between border-t border-rule bg-raised/60 px-3.5 py-2 text-tiny text-muted max-sm:hidden"
    >
      <span class="flex items-center gap-2">
        <span class="inline-flex items-center gap-1"
          ><kbd class={kbd}>↑</kbd><kbd class={kbd}>↓</kbd> navigate</span
        >
        <span class="inline-flex items-center gap-1"><kbd class={kbd}>↵</kbd> open</span>
      </span>
      <span class="inline-flex items-center gap-1"><kbd class={kbd}>⌘K</kbd> switcher</span>
    </div>
  </div>
</div>
