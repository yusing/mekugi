<script lang="ts">
  import Path from "./Path.svelte";
  import { basename, statusText } from "./format";
  import { hub, type Machine } from "./hub.svelte";
  import Mark from "./Mark.svelte";
  import type { Listed } from "./protocol";
  import { selectionHash } from "./transport";
  import Button from "./ui/Button.svelte";
  import Dot from "./ui/Dot.svelte";
  import Icon from "./ui/Icon.svelte";
  import { kbd, skeleton } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";
  import { appearance, themes, type Theme } from "./theme.svelte";
  import { select, view } from "./view.svelte";

  // Every connected machine's threads, grouped under the machine. The footer
  // opens machine settings and chooses the page's theme; a machine whose
  // token was refused offers its replacement in place.
  let {
    onhide,
    onmachines,
    onswitcher,
    onnavigate,
  }: {
    onhide: () => void;
    onmachines: (machine?: Machine) => void;
    onswitcher?: () => void;
    onnavigate: () => void;
  } = $props();
  let collapsed = $state<Record<string, boolean>>({});
  const name = (s: Listed) =>
    s.title || (s.workspace ? basename(s.workspace) : undefined) || s.thread || "New thread";
  function start(machine?: Machine) {
    select(
      machine?.id ??
        view.machine ??
        hub.machines.find((m) => m.connection === "open")?.id ??
        hub.machines[0]?.id,
    );
    onnavigate();
    queueMicrotask(() => document.getElementById("new-text")?.focus());
  }
  // The theme button shows the current choice and moves to the next.
  const themeIcons = { system: "monitor", light: "sun", dark: "moon" } as const;
  const title = (theme: Theme) => theme[0].toUpperCase() + theme.slice(1);
  // The sidebar's rows: quiet hover on the tinted rail, crisp elevated surface when active.
  const row =
    "flex w-full items-center rounded-control text-left text-ink transition-colors hover:bg-active/80 aria-[current=page]:bg-surface aria-[current=page]:shadow-xs aria-[current=page]:ring-1 aria-[current=page]:ring-border max-sm:min-h-11";
</script>

<nav class="flex h-full min-h-0 flex-col px-2.5 pt-safe pb-safe" aria-label="Threads">
  <div class="flex h-12 items-center justify-between pl-1.5">
    <span class="flex items-center gap-2 font-semibold tracking-[-0.01em]"><Mark />Mekugi</span>
    <div class="flex items-center gap-0.5">
      {#if onswitcher}
        <Button
          variant="quiet"
          icon="search"
          label="Quick switcher"
          title="Quick switcher (⌘K)"
          tooltip
          onclick={onswitcher}
        />
      {/if}
      <Button
        variant="quiet"
        icon="sidebar"
        label="Hide sidebar"
        title="Hide sidebar (⌘\)"
        tooltip
        onclick={onhide}
      />
    </div>
  </div>
  <button
    type="button"
    class="flex min-h-9 w-full items-center gap-2.5 rounded-control border border-border bg-surface px-2.5 text-left font-medium text-ink shadow-xs transition-colors hover:bg-raised aria-[current=page]:border-control aria-[current=page]:bg-surface max-sm:min-h-11"
    aria-current={!view.session && !!view.machine ? "page" : undefined}
    onclick={() => start()}
  >
    <Icon name="compose" size={15} />
    <span class="flex-1 truncate">New thread</span>
    {#if onswitcher}<kbd class="{kbd} max-sm:hidden">⌘K</kbd>{/if}
  </button>

  <div class="mt-3 min-h-0 flex-1 overflow-y-auto overscroll-contain">
    {#each hub.machines as machine (machine.id)}
      {@const state = machine.state}
      {@const open = !collapsed[machine.id]}
      <section class="group/machine not-first:mt-3.5" aria-labelledby="machine-{machine.id}">
        <div class="flex items-center gap-0.5">
          <button
            type="button"
            id="machine-{machine.id}"
            class="flex min-h-8 min-w-0 flex-1 items-center gap-2 rounded-control px-1.5 text-left text-small font-semibold text-muted transition-colors hover:bg-active/80 hover:text-ink max-sm:min-h-11"
            aria-label="{machine.label}, {state.text}"
            aria-expanded={open}
            onclick={() => (collapsed[machine.id] = open)}
          >
            <span class={["inline-flex transition-transform duration-150", !open && "-rotate-90"]}
              ><Icon name="chevron" size={14} /></span
            >
            <Dot tone={state.tone || "idle"} />
            <span class="min-w-0 flex-1 truncate">{machine.label}</span>
            {#if machine.sessions?.length}
              <span class="text-tiny font-normal text-muted tabular-nums" aria-hidden="true"
                >{machine.sessions.length}</span
              >
            {/if}
          </button>
          <Button
            variant="quiet"
            icon="plus"
            size="sm"
            label="New thread on {machine.label}"
            class="opacity-0 transition-opacity group-hover/machine:opacity-100 focus-visible:opacity-100 touch:opacity-100"
            onclick={() => start(machine)}
          />
        </div>
        {#if state.tone === "alert" || state.tone === "warn"}
          <p
            class={[
              "mb-1 ml-8.5 text-small",
              state.tone === "alert" ? "text-negative" : "text-warning-ink",
            ]}
          >
            {state.text}{#if machine.connection === "required"}
              <button type="button" class="ml-2 underline" onclick={() => onmachines(machine)}
                >Replace token</button
              >{/if}
          </p>
        {/if}
        {#if open}
          {#if !machine.sessions}
            {#if machine.connection === "connecting"}<div
                class="{skeleton} mt-2 mr-2.5 ml-7 h-5"
                aria-label="Loading threads"
              ></div>{/if}
          {:else if !machine.sessions.length}
            <p class="mt-0.5 py-1.5 pr-2.5 pl-7 text-small text-muted">No open threads</p>
          {:else}
            <ul class="mt-1 space-y-0.5">
              {#each machine.sessions as s (s.id)}
                {@const status = statusText(s.status)}
                {@const current = view.machine === machine.id && view.session === s.id}
                <li>
                  <a
                    class="{row} min-h-[50px] gap-2 py-1.5 pr-2.5 pl-6.5 no-underline aria-[current=page]:font-medium"
                    href={selectionHash({ machine: machine.id, session: s.id })}
                    aria-current={current ? "page" : undefined}
                    title={[name(s), s.workspace, status].filter(Boolean).join("\n")}
                    onclick={onnavigate}
                  >
                    <span class="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span class="truncate leading-[19px]"
                        >{#if !s.title && s.workspace}<Path
                            path={name(s)}
                            title={s.workspace}
                          />{:else}{name(s)}{/if}</span
                      >
                      {#if s.workspace}<span class="truncate text-small font-normal text-muted"
                          ><Path path={basename(s.workspace)} title={s.workspace} /></span
                        >{/if}
                    </span>
                    {#if s.replay}<Tag>Replay</Tag>{/if}
                    {#if s.waiting}<span
                        class="min-w-[18px] flex-none rounded-full bg-warning-tint px-[5px] text-center text-tiny text-warning-ink tabular-nums ring-1 ring-warning-edge ring-inset"
                        title="Prompts waiting">{s.waiting}</span
                      >{/if}
                    <Dot tone={s.status.alert ? "alert" : s.status.running ? "running" : "none"} />
                    {#if status || s.waiting}<span class="sr-only"
                        >{status}{s.waiting ? `, ${s.waiting} waiting` : ""}</span
                      >{/if}
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        {/if}
      </section>
    {:else}
      <p class="mt-0.5 py-1.5 pr-2.5 pl-6.5 text-small text-muted">
        Connect a machine to see its threads here.
      </p>
    {/each}
  </div>

  <div class="mt-2 border-t border-rule py-2">
    <div class="flex items-center gap-1">
      <button
        type="button"
        class="{row} min-h-9 min-w-0 flex-1 gap-2.5 px-2.5 font-medium"
        onclick={() => onmachines()}
      >
        <Icon name="server" size={15} />Machines
        {#if hub.machines.length}<span
            class="ml-auto rounded-full bg-active px-1.5 py-px text-tiny text-muted tabular-nums"
            aria-hidden="true">{hub.machines.length}</span
          >{/if}
      </button>
      <Button
        variant="quiet"
        icon={themeIcons[appearance.choice]}
        label="Theme: {title(appearance.choice)}"
        tooltip
        onclick={() =>
          appearance.set(themes[(themes.indexOf(appearance.choice) + 1) % themes.length])}
      />
    </div>
    {#if appearance.storageError}<p role="status" class="mt-1 px-2.5 text-small text-muted">
        {appearance.storageError}
      </p>{/if}
  </div>
</nav>
