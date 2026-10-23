<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import { onDestroy, tick, untrack } from "svelte";
  import Attachments from "./Attachments.svelte";
  import { prepareAttachments, type InitialInput, type PendingAttachment } from "./attachments";
  import { basename } from "./format";
  import { hub, type Machine } from "./hub.svelte";
  import type { Start, StartOptions } from "./protocol";
  import Button from "./ui/Button.svelte";
  import ComposerCard from "./ui/ComposerCard.svelte";
  import Icon from "./ui/Icon.svelte";
  import Menu from "./ui/Menu.svelte";
  import MenuItem from "./ui/MenuItem.svelte";
  import Picker from "./ui/Picker.svelte";
  import { chip, composerText, composerTools, input, menuRule } from "./ui/styles";
  import { open, select } from "./view.svelte";

  // A new thread starts from its first message. The toolbar picks where it
  // runs and with which model; the + menu also resumes or replays a saved
  // thread by its ID.
  let {
    machine,
    onopened,
  }: {
    machine: Machine;
    onopened: (machine: string, session: string, notice?: string, initial?: InitialInput) => void;
  } = $props();
  let mode = $state<"new" | "resume" | "replay">("new");
  let options = $state<StartOptions>();
  let loaded = $state<string>();
  let loadError = $state<string>();
  let workspace = $state("");
  let model = $state("");
  let effort = $state("");
  let text = $state("");
  let saved = $state("");
  let speed = $state(1);
  let items = $state<PendingAttachment[]>([]);
  let pathing = $state(false);
  let error = $state<string>();
  let progress = $state("");
  let controller = $state<AbortController>();
  let tray = $state<ReturnType<typeof Attachments>>();
  let idField = $state<HTMLInputElement>();
  let disposed = false;
  const touch = matchMedia("(pointer: coarse)").matches;
  const chosen = $derived(
    options?.models.flatMap((group) => group.models).find((choice) => choice.id === model),
  );
  const connected = $derived(machine.connection === "open");
  const busy = $derived(!!controller);
  onDestroy(() => {
    disposed = true;
    controller?.abort();
  });

  // Workspaces and models belong to the machine, so each machine loads its own
  // once it answers.
  $effect(() => {
    const target = machine;
    if (target.connection !== "open" || loaded === target.id) return;
    untrack(() => load(target));
  });
  async function load(target: Machine) {
    loaded = target.id;
    loadError = undefined;
    options = undefined;
    const result = await hub.startOptions(target);
    if (disposed || machine !== target) return;
    if ("error" in result) {
      loadError = result.error;
      loaded = undefined;
      return;
    }
    options = result.options;
    workspace = options.workspaces[0] ?? "";
    model = options.defaultModel;
    effort = "";
  }

  async function begin(next: "resume" | "replay") {
    mode = next;
    error = undefined;
    await tick();
    idField?.focus();
  }
  async function start() {
    if (controller || !connected) return;
    const pending = new AbortController();
    controller = pending;
    error = undefined;
    const target = machine;
    try {
      let request: Start;
      let initial: InitialInput | undefined;
      if (mode === "replay") request = { replay: { session: saved.trim(), speed } };
      else {
        if (!options) throw new Error(`Wait for ${target.label}'s workspaces and models.`);
        if (mode === "new" && !text.trim() && !items.length)
          throw new Error("Write a first message or attach something.");
        if (/^[!/]/.test(text.trimStart()))
          throw new Error(
            "Start with a message. Slash commands and shell input work once the thread runs.",
          );
        const attachments = await prepareAttachments(
          items,
          pending.signal,
          (read, total) =>
            (progress = total
              ? `Reading attachments: ${Math.round((read / total) * 100)}%`
              : "Preparing input…"),
        );
        request = {
          workspace,
          model,
          effort: effort || undefined,
          resume: mode === "resume" ? saved.trim() : undefined,
        };
        if (text.trim() || attachments.length) initial = { text, items: [...items], attachments };
      }
      pending.signal.throwIfAborted();
      progress =
        mode === "replay"
          ? "Opening the recording…"
          : mode === "resume"
            ? "Resuming the thread…"
            : "Starting the thread…";
      const result = await hub.createSession(request, target, pending.signal);
      if (disposed) return;
      if ("error" in result) {
        error = result.error;
        return;
      }
      select(target.id);
      open(result.id);
      onopened(target.id, result.id, result.notice, initial);
    } catch (failure) {
      if (!disposed)
        error = pending.signal.aborted
          ? "Cancelled. Work the machine already admitted may continue."
          : failure instanceof Error
            ? failure.message
            : "Could not prepare attachments.";
    } finally {
      controller = undefined;
      progress = "";
    }
  }
  function onkeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing && !touch) {
      event.preventDefault();
      void start();
    }
  }
  const ready = $derived(
    connected &&
      !busy &&
      (mode === "replay"
        ? !!saved.trim()
        : !!options &&
          !!workspace.trim() &&
          (mode === "resume" ? !!saved.trim() : !!text.trim() || !!items.length)),
  );
  // On phones the workspace picker takes its own row and the others share
  // the next one with the menu and Start.
</script>

<section
  class="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)] content-center overflow-y-auto px-4 pt-6 pb-[12dvh] max-sm:content-end max-sm:px-2 max-sm:pt-4 max-sm:pb-safe-2"
  aria-labelledby="new-heading"
>
  <div class="mx-auto w-full max-w-column">
    <h1
      id="new-heading"
      class="mb-5 text-center text-heading font-semibold focus:outline-none max-sm:mb-auto max-sm:pb-6"
      tabindex="-1"
    >
      {mode === "replay"
        ? "Replay a saved thread"
        : mode === "resume"
          ? "Resume a saved thread"
          : "What should main work on?"}
    </h1>
    <ComposerCard
      accepts={mode !== "replay" && !busy}
      onfiles={(files) => tray?.add(files)}
      onsubmit={() => void start()}
    >
      {#if mode !== "new"}
        <div class="flex items-center gap-2 pt-2.5 pr-2 pl-3.5 text-muted">
          <Icon name={mode === "replay" ? "play" : "history"} />
          <label class="min-w-0 flex-1"
            ><span class="sr-only">Saved session ID</span><input
              class="{input} font-mono"
              bind:this={idField}
              bind:value={saved}
              placeholder="Saved session ID"
              disabled={busy}
              required
              autocomplete="off"
              spellcheck="false"
            /></label
          >
          {#if mode === "replay"}
            <label class="flex items-center gap-1.5 text-small"
              ><span>Speed</span><input
                class="{input} max-w-[72px] tabular-nums"
                type="number"
                min="0.1"
                max="100"
                step="0.1"
                required
                bind:value={speed}
                disabled={busy}
                aria-label="Playback speed"
              /></label
            >
          {/if}
          <Button
            variant="quiet"
            icon="close"
            label={mode === "replay" ? "Cancel replay" : "Cancel resume"}
            disabled={busy}
            onclick={() => (mode = "new")}
          />
        </div>
      {/if}
      {#if mode !== "replay"}
        <Attachments
          bind:this={tray}
          bind:items
          bind:pathing
          machine={machine.label}
          disabled={busy}
        />
        <label class="sr-only" for="new-text">First message</label>
        <textarea
          id="new-text"
          class="{composerText} min-h-18 pt-3.5 pb-1"
          bind:value={text}
          {onkeydown}
          onpaste={(event) => {
            const files = event.clipboardData?.files;
            if (!files?.length || busy) return;
            if (!event.clipboardData?.types.includes("text/plain")) event.preventDefault();
            tray?.add(files);
          }}
          rows="2"
          disabled={busy}
          placeholder={mode === "resume"
            ? "Message to continue with (optional)"
            : `Message main on ${machine.label}`}></textarea>
      {:else}
        <p class="px-3.5 pt-2.5 pb-1 text-small text-muted">
          Replays use {machine.label}'s saved history. The live thread must have ended; model text
          is simulated and no tools run.
        </p>
      {/if}
      <div class="{composerTools} flex-wrap gap-1">
        <Menu label="More ways to start" icon="plus" disabled={busy}>
          {#snippet children(close)}
            <MenuItem
              icon="attach"
              disabled={mode === "replay"}
              onclick={() => {
                close();
                tray?.pick();
              }}>Attach files</MenuItem
            >
            <MenuItem
              icon="folder"
              disabled={mode === "replay"}
              onclick={() => {
                close();
                pathing = true;
              }}>Attach path on {machine.label}</MenuItem
            >
            <div class={menuRule} role="separator"></div>
            <MenuItem
              icon="history"
              onclick={() => {
                close();
                void begin("resume");
              }}>Resume saved thread</MenuItem
            >
            <MenuItem
              icon="play"
              onclick={() => {
                close();
                void begin("replay");
              }}>Open replay</MenuItem
            >
          {/snippet}
        </Menu>
        <Picker
          class="max-sm:flex-1"
          label="Machine"
          title="Execution machine"
          icon="server"
          value={machine.id}
          groups={[
            {
              choices: hub.machines.map((candidate) => ({
                value: candidate.id,
                label: candidate.label,
              })),
            },
          ]}
          disabled={busy}
          onchange={select}
        />
        {#if mode !== "replay"}
          {#if options}
            <!-- A path shows its folder name; the list and title show it whole. -->
            <Picker
              class="max-sm:order-first max-sm:basis-full"
              label="Workspace on {machine.label}"
              title={workspace}
              icon="folder"
              entry="Workspace path"
              text={basename(workspace) || "Choose a workspace"}
              bind:value={workspace}
              groups={[
                {
                  choices: options.workspaces.map((path) => ({
                    value: path,
                    label: basename(path),
                    detail: path,
                  })),
                },
              ]}
              disabled={busy}
            />
            <Picker
              class="max-sm:flex-1"
              label="Model"
              bind:value={model}
              groups={options.models.map((group) => ({
                label: group.label,
                choices: group.models.map((choice) => ({ value: choice.id, label: choice.label })),
              }))}
              disabled={busy}
              onchange={() => (effort = "")}
            />
            {#if chosen?.efforts.length}
              <Picker
                class="max-sm:flex-1"
                label="Reasoning effort"
                bind:value={effort}
                groups={[
                  {
                    choices: [
                      {
                        value: "",
                        label: chosen.defaultEffort
                          ? `${chosen.defaultEffort} (default)`
                          : "Default effort",
                      },
                      ...chosen.efforts
                        .filter((value) => value !== chosen.defaultEffort)
                        .map((value) => ({ value, label: value })),
                    ],
                  },
                ]}
                disabled={busy}
              />
            {/if}
          {:else if connected && !loadError}
            <span class="{chip} border-dashed" role="status">Loading workspaces and models…</span>
          {/if}
        {/if}
        <span class="flex-1 max-sm:hidden"></span>
        {#if busy}<Button variant="quiet" onclick={() => controller?.abort()}>Cancel</Button>{/if}
        <Button
          type="submit"
          variant="primary"
          shape="round"
          icon={mode === "replay" ? "play" : "send"}
          label={mode === "replay"
            ? "Open replay"
            : mode === "resume"
              ? "Resume thread"
              : "Start thread"}
          disabled={!ready}
        />
      </div>
    </ComposerCard>
    <div class="min-h-8 px-1 pt-2 text-small" aria-live="polite">
      {#if error}<Alert>{error}</Alert>
      {:else if progress}<p class="text-muted">{progress}</p>
      {:else if !connected}<p
          class={machine.state.tone === "alert" ? "text-negative" : "text-muted"}
        >
          {machine.label}: {machine.state.text}. {machine.error ?? ""}
        </p>
      {:else if loadError}<p class="text-negative">
          {loadError}
          <button type="button" class="underline" onclick={() => load(machine)}>Retry</button>
        </p>
      {:else if options?.note && mode !== "replay"}<p class="text-muted">{options.note}</p>{/if}
    </div>
  </div>
</section>
