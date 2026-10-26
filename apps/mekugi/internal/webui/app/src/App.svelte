<script lang="ts">
  import { tick } from "svelte";
  import Alert from "./lib/ui/Alert.svelte";
  import type { InitialInput } from "./lib/attachments";
  import { hub, type Machine } from "./lib/hub.svelte";
  import MachineForm from "./lib/MachineForm.svelte";
  import Mark from "./lib/Mark.svelte";
  import MachineSettings from "./lib/MachineSettings.svelte";
  import NewThread from "./lib/NewThread.svelte";
  import Sidebar from "./lib/Sidebar.svelte";
  import ThreadView from "./lib/ThreadView.svelte";
  import Button from "./lib/ui/Button.svelte";
  import Dialog from "./lib/ui/Dialog.svelte";
  import Notice from "./lib/ui/Notice.svelte";
  import { floating } from "./lib/ui/styles";
  import { view } from "./lib/view.svelte";

  // The sidebar sits beside the main area where it fits and is a drawer over
  // it otherwise.
  const media = matchMedia("(min-width: 900px)");
  let wide = $state(media.matches);
  let sidebar = $state(media.matches);
  $effect(() => {
    const change = () => (wide = sidebar = media.matches);
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  });

  let settings = $state<{ focus?: Machine }>();
  let endTarget = $state<{
    machine: Machine;
    id: string;
    title: string;
    replay: boolean;
    pending: boolean;
    error?: string;
  }>();
  let endNotice = $state<string>();
  let creationNotice = $state<{ machine: string; session: string; text: string }>();
  let firstInput = $state<{ machine: string; session: string; input: InitialInput }>();
  async function endSession() {
    const target = endTarget;
    if (!target || target.pending) return;
    target.pending = true;
    const prefix = `${target.title} on ${target.machine.label}`;
    endNotice = `Ending ${prefix}… You can keep using other threads.`;
    target.error = await hub.endSession(target.id, target.machine);
    target.pending = false;
    endNotice = target.error ? `End request for ${prefix}: ${target.error}` : `Ended ${prefix}.`;
    if (!target.error && endTarget === target) endTarget = undefined;
  }

  // A link to a thread on a machine this page has not connected opens a new
  // thread on a connected machine instead.
  const machine = $derived(hub.selected ?? hub.machines[0]);
  const thread = $derived(hub.selected && view.session ? view.session : undefined);
  const hide = () => {
    if (!wide) sidebar = false;
  };
  // Below the breakpoint the sidebar is a modal drawer: it takes focus, the
  // sheet behind it is inert, and Escape or the scrim closes it, returning
  // focus to the button that opened it.
  const modal = $derived(sidebar && !wide);
  let drawer = $state<HTMLElement>();
  $effect(() => {
    if (modal) drawer?.querySelector<HTMLElement>("[aria-current=page], a, button")?.focus();
  });
  function closeDrawer() {
    sidebar = false;
    void tick().then(() =>
      document.querySelector<HTMLElement>('button[aria-label="Show sidebar"]')?.focus(),
    );
  }
</script>

<svelte:window
  onkeydown={(event) => {
    if (modal && event.key === "Escape" && !event.defaultPrevented) {
      event.preventDefault();
      closeDrawer();
    }
  }}
/>

<div class="relative flex h-dvh">
  {#if sidebar}
    <div
      bind:this={drawer}
      class={wide
        ? "min-h-0 w-sidebar flex-none bg-ground"
        : "fixed inset-y-0 left-0 z-30 w-[min(var(--container-sidebar),86vw)] border-r bg-surface shadow-float"}
    >
      <Sidebar
        onhide={() => (modal ? closeDrawer() : (sidebar = false))}
        onmachines={(focus) => {
          hide();
          settings = { focus };
        }}
        onnavigate={hide}
      />
    </div>
    {#if !wide}
      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (the sidebar's own button also hides it) -->
      <div class="fixed inset-0 z-29 bg-scrim" onclick={closeDrawer}></div>
    {/if}
  {/if}

  <!-- The main area is a sheet beside the sidebar, inset by the frame. On a
       phone the frame disappears and the sheet becomes the screen. -->
  <div
    class={[
      "relative my-2 mr-2 flex min-w-0 flex-1 flex-col overflow-hidden rounded-sheet border bg-surface max-sm:m-0 max-sm:rounded-none max-sm:border-0",
      sidebar && wide ? "ml-0" : "ml-2",
    ]}
    inert={modal}
  >
    {#if hub.storageError && !settings}<Notice role="alert" class="m-3 flex-none"
        >{hub.storageError}</Notice
      >{/if}
    {#if machine && thread}
      {#key `${machine.id}:${thread}`}
        <ThreadView
          {machine}
          id={thread}
          {sidebar}
          onsidebar={() => (sidebar = true)}
          initial={firstInput?.machine === machine.id && firstInput.session === thread
            ? firstInput.input
            : undefined}
          oninitial={() => (firstInput = undefined)}
          notice={creationNotice?.machine === machine.id && creationNotice.session === thread
            ? creationNotice.text
            : undefined}
          onend={(target) => (endTarget = { ...target, pending: false })}
        />
      {/key}
    {:else}
      <header class="flex h-bar flex-none items-center px-2 pt-safe">
        {#if !sidebar}<Button
            variant="quiet"
            icon="sidebar"
            label="Show sidebar"
            onclick={() => (sidebar = true)}
          />{/if}
      </header>
      {#if machine}
        {#if view.session && !hub.selected}<Notice class="mx-auto w-[calc(100%-2rem)] max-w-column">
            That thread's machine is not connected in this page. Add it under Machines to open the
            thread.
          </Notice>{/if}
        <NewThread
          {machine}
          onopened={(machine, session, text, initial) => {
            creationNotice = text ? { machine, session, text } : undefined;
            firstInput = initial ? { machine, session, input: initial } : undefined;
          }}
        />
      {:else}
        <section
          class="grid min-h-0 flex-1 items-start justify-items-center overflow-y-auto px-4 pt-[10dvh] pb-6"
          aria-labelledby="welcome-heading"
        >
          <div class="grid w-[min(100%,440px)] gap-3">
            <Mark size={28} />
            <h1 id="welcome-heading" class="mt-1 text-heading font-semibold">Connect a machine</h1>
            <p class="mb-2 text-muted">
              Threads run on your execution machines. Connect one through its connector to start a
              thread or follow work already running there.
            </p>
            <MachineForm />
          </div>
        </section>
      {/if}
    {/if}
    {#if endNotice}
      <div
        class="{floating} absolute top-[calc(var(--topbar)+env(safe-area-inset-top,0px)+8px)] left-1/2 z-20 flex max-w-[calc(100%-2rem)] -translate-x-1/2 items-center gap-2 py-1 pr-1 pl-3.5 text-small"
        role="status"
      >
        <span>{endNotice}</span>
        <Button
          variant="quiet"
          icon="close"
          label="Dismiss"
          onclick={() => (endNotice = undefined)}
        />
      </div>
    {/if}
  </div>
</div>

{#if settings}
  <MachineSettings focus={settings.focus} onclose={() => (settings = undefined)} />
{/if}
{#if endTarget}
  <Dialog
    title={endTarget.replay ? "End replay?" : "End thread?"}
    onclose={() => (endTarget = undefined)}
  >
    <p class="mb-2">
      End <strong class="font-semibold">{endTarget.title}</strong> on
      <strong class="font-semibold">{endTarget.machine.label}</strong>? Active work stops. Saved
      history stays available to resume or replay.
    </p>
    <p class="font-mono text-small text-muted">{endTarget.id}</p>
    {#if endTarget.error}<Alert class="mt-2">{endTarget.error}</Alert>{/if}
    <div class="mt-4 flex justify-end gap-2">
      <Button variant="quiet" onclick={() => (endTarget = undefined)}
        >{endTarget.pending ? "Dismiss" : "Keep open"}</Button
      >
      <Button variant="danger" disabled={endTarget.pending} onclick={endSession}
        >{endTarget.pending ? "Ending…" : "End"}</Button
      >
    </div>
  </Dialog>
{/if}
