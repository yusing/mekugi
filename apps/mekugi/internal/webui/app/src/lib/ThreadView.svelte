<script lang="ts">
  import { tick, untrack } from "svelte";
  import { stateLabel, worked } from "./agents";
  import type { InitialInput } from "./attachments";
  import Composer from "./Composer.svelte";
  import DetailsPanel from "./DetailsPanel.svelte";
  import DiffPanel from "./DiffPanel.svelte";
  import LiveSource from "./LiveSource.svelte";
  import { basename, duration, statusText } from "./format";
  import type { Machine } from "./hub.svelte";
  import NativeTranscript from "./NativeTranscript.svelte";
  import PlaybackBar from "./PlaybackBar.svelte";
  import Prompt from "./Prompt.svelte";
  import type { Action, Agent } from "./protocol";
  import Roster from "./Roster.svelte";
  import { semantic } from "./semantic.svelte";
  import SemanticDialogs from "./SemanticDialogs.svelte";
  import SessionInfo from "./SessionInfo.svelte";
  import { stream, Stream } from "./stream.svelte";
  import { newIdentity } from "./transport";
  import ThreadDocks from "./ThreadDocks.svelte";
  import Transcript from "./Transcript.svelte";
  import Button from "./ui/Button.svelte";
  import Dot from "./ui/Dot.svelte";
  import Icon from "./ui/Icon.svelte";
  import Notice from "./ui/Notice.svelte";
  import Popover from "./ui/Popover.svelte";
  import { floating, segment, segments, skeleton } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";
  import { show, view } from "./view.svelte";

  // One thread: its transcript, the composer or playback controls at the
  // foot, and a right panel whose tabs hold the agents, the session's changes
  // and one activity's details.
  let {
    machine,
    id,
    initial,
    oninitial,
    notice: opened,
    sidebar,
    onsidebar,
    onend,
  }: {
    machine: Machine;
    id: string;
    initial?: InitialInput;
    oninitial: () => void;
    notice?: string;
    sidebar: boolean;
    onsidebar: () => void;
    onend: (target: { machine: Machine; id: string; title: string; replay: boolean }) => void;
  } = $props();

  $effect(() => {
    untrack(() => stream.connect(id, machine));
    return () => stream.close();
  });
  $effect(() => {
    const active = live && !ended;
    untrack(() => {
      if (active) semantic.connect(id, machine);
      else if (ended || playback) semantic.release(id, machine);
    });
  });
  $effect(() => {
    const thread = id,
      owner = machine;
    return () => semantic.release(thread, owner);
  });

  const session = $derived(stream.session);
  const gone = $derived(stream.gone);
  // A session shown here that no longer answers has ended.
  const ended = $derived(!!gone && !!session);
  const playback = $derived(stream.playback);
  const live = $derived(!!session && !playback);
  const start = $derived(session?.started ?? 0);
  const root = $derived(stream.agent("/root"));
  const title = $derived(
    session?.title || (session?.workspace ? basename(session.workspace) : "Thread"),
  );
  $effect(() => {
    document.title = session ? `${title} · Mekugi` : "Mekugi";
    return () => (document.title = "Mekugi");
  });
  const statusLabel = $derived(ended ? "Ended" : statusText(stream.status));
  // The header names the state; a failure's message opens beneath it on request.
  const failure = $derived(ended ? undefined : stream.status.failure);
  const headline = $derived(
    ended ? "Ended" : statusText({ ...stream.status, failure: undefined }) || "Failed",
  );
  const failureID = `failure-${newIdentity()}`;
  let failureOpen = $state(false);
  const main = $derived(stream.main.order.map((seq) => stream.main.entries[seq]));

  // Arriving from the thread list moves focus to the thread's heading.
  let heading = $state<HTMLElement>();
  $effect(() => {
    const from = document.activeElement;
    if (!from || from === document.body || from.closest("nav"))
      tick().then(() => heading?.focus({ preventScroll: true }));
  });

  // The transcript shows the chosen agent's own activity, and Main's otherwise.
  const shown = $derived(
    view.agent && view.agent !== "/root" ? stream.agent(view.agent) : undefined,
  );
  // Seeking back to before the chosen agent started returns to Main for good,
  // rather than switching again once the agent starts.
  $effect(() => {
    if (view.agent && stream.roster.agents.length && !shown) show(undefined, true);
  });
  const owner = $derived(shown?.name ?? "/root");
  const child = new Stream();
  const childThread = $derived(
    shown?.thread && semantic.root?.frame.childThreads?.includes(shown.thread)
      ? shown.thread
      : undefined,
  );
  const childReady = $derived(!!childThread && semantic.frame?.view.Main.ID === childThread);
  const childFeedReady = $derived(childReady && child.session?.thread === childThread);
  const active = $derived(childThread ? child : stream);
  $effect(() => {
    const thread = live && !ended ? childThread : undefined;
    untrack(() => semantic.select(thread));
  });
  $effect(() => {
    const thread = childReady ? childThread : undefined;
    untrack(() => {
      if (thread) child.connect(thread, machine);
      else child.close();
    });
    return () => child.close();
  });
  const entries = $derived(
    childFeedReady
      ? child.main.order.map((seq) => child.main.entries[seq])
      : shown
        ? stream.agents.order
            .map((seq) => stream.agents.entries[seq])
            .filter((e) => e.agent === shown.name)
        : main,
  );
  // A view changed from inside the transcript removes the control that changed
  // it; focus then moves to the new transcript, which names whose it is.
  let feed = $state<HTMLElement>();
  let viewed: string | undefined;
  $effect(() => {
    const current = owner;
    const changed = viewed !== undefined && viewed !== current;
    viewed = current;
    if (changed && feed && (!document.activeElement || document.activeElement === document.body))
      feed.focus({ preventScroll: true });
  });

  // The recorded clock during replay; agents' timers count on it.
  let wall = $state(Date.now());
  $effect(() => {
    if (playback) return;
    const timer = setInterval(() => (wall = Date.now()), 1000);
    return () => clearInterval(timer);
  });
  const now = $derived(playback ? start + playback.position : wall);
  // An agent's newest entry describes its current work only if it came after
  // that work started; a new task otherwise shows the end of the old one.
  const activity = (agent: Agent) => {
    const entry = agent.activity;
    return entry && (!agent.workSince || (entry.observed ?? 0) >= agent.workSince)
      ? entry
      : undefined;
  };

  // The right panel is open by default where it fits beside the transcript.
  const wide = matchMedia("(min-width: 1100px)");
  let panelOpen = $state(wide.matches);
  $effect(() => {
    const change = () => (panelOpen = wide.matches);
    wide.addEventListener("change", change);
    return () => wide.removeEventListener("change", change);
  });
  const frame = $derived(semantic.frame);
  const sview = $derived(frame?.view);
  const details = $derived(live && !ended ? frame?.details : undefined);
  // Saved changes exist for live sessions; recorded playback has none.
  const changes = $derived(!!stream.review);
  const net = $derived(stream.review?.counts.Net);
  type Tab = "agents" | "changes" | "details";
  let chosen = $state<Tab>("agents");
  // Requested details show at once; once closed, the tab shown before returns.
  let before: Tab = "agents";
  $effect(() => {
    if (details?.view.ID.Selection === undefined) return;
    untrack(() => {
      if (chosen !== "details") before = chosen;
      chosen = "details";
      panelOpen = true;
    });
  });
  const tab = $derived.by(() => {
    const current = chosen === "details" && !details ? before : chosen;
    return current === "changes" && !changes ? "agents" : current;
  });
  const panel = $derived(panelOpen ? tab : undefined);
  function select(name: string) {
    show(name);
    if (!wide.matches) panelOpen = false;
  }
  function closeDetails() {
    if (details) void semantic.send({ closeDetails: details.view.ID });
  }
  function closePanel() {
    closeDetails();
    panelOpen = false;
  }
  // A header toggle opens its tab, or closes the panel showing it.
  function toggle(target: Tab) {
    if (panel === target) closePanel();
    else {
      chosen = target;
      panelOpen = true;
    }
  }

  const side = $derived(
    (!shown || childFeedReady) && !!sview && sview.Displayed.ID !== sview.Main.ID,
  );
  const tabs = $derived((!shown || childFeedReady) && !!sview && (!!sview.Side || side));

  const notice = $derived.by(() => {
    if (gone) return ended ? "This thread has ended. Its last state stays here." : undefined;
    if (childThread && child.gone) return child.gone.text;
    if (active.connection === "open") return active.controlError;
    if (active.error) return active.error;
    if (active.connection === "lost") return "Connection to this machine lost. Retrying…";
    return session ? "Connection lost. Reconnecting…" : undefined;
  });

  // Each feed keeps only its newest rows, so an agent that finished long ago
  // may have none left.
  const empty = $derived(
    shown?.turns && shown.state !== "working"
      ? `${shown.label}'s activity is no longer kept; the transcript keeps only the newest.`
      : live
        ? shown
          ? `Nothing from ${shown.label} yet.`
          : ended
            ? "Nothing happened in this thread."
            : "Nothing here yet. Send main a message to start."
        : shown
          ? `Nothing from ${shown.label} yet at this point.`
          : playback?.paused
            ? "Nothing recorded yet at this point. Press play or drag along the clock."
            : "Nothing recorded yet at this point.",
  );

  const keys: Record<string, Action> = {
    " ": "toggle-pause",
    p: "toggle-pause",
    "+": "faster",
    "=": "faster",
    "-": "slower",
    "[": "back",
    "]": "forward",
    r: "restart",
  };
  function onkeydown(event: KeyboardEvent) {
    if (event.key === "Escape" && panel && !wide.matches && !event.defaultPrevented) {
      const target = event.target as HTMLElement;
      if (!target.closest("dialog, [popover], textarea, input")) {
        event.preventDefault();
        closePanel();
        return;
      }
    }
    if (event.metaKey || event.ctrlKey || event.altKey || !playback || !session) return;
    const target = event.target as HTMLElement;
    if (target.closest("input, textarea, select, dialog, [contenteditable=true]")) return;
    if (event.key === " " && target.closest("button, [role=slider], summary")) return;
    let action = keys[event.key];
    if (!action) return;
    event.preventDefault();
    // At the end, playing again means starting over, as the play button does.
    if (action === "toggle-pause" && playback.position >= playback.until) action = "restart";
    stream.control(action);
  }

  let copied = $state(false);
  async function copyResume() {
    try {
      await navigator.clipboard.writeText(`mekugi resume ${id}`);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      semantic.copied = `mekugi resume ${id}`;
    }
  }
  // Notices, tabs and the agent bar share the composer's column.
  const column = "mx-auto w-[calc(100%-32px)] max-w-column";
</script>

<svelte:window {onkeydown} />

<div
  class="grid min-h-0 min-w-0 flex-1 grid-cols-[minmax(0,1fr)] grid-rows-[calc(var(--topbar)+env(safe-area-inset-top,0px))_minmax(0,1fr)]"
>
  <header
    class="flex min-w-0 items-center gap-2 border-b border-rule pt-safe pr-2 pl-4 max-sm:gap-1 max-sm:px-1"
  >
    {#if !sidebar}<Button
        variant="quiet"
        icon="sidebar"
        label="Show sidebar"
        onclick={onsidebar}
      />{/if}
    <!-- One element throughout, so focus moved here stays as it loads. -->
    <h1
      bind:this={heading}
      class="max-w-[50%] min-w-0 flex-none truncate text-base font-semibold focus:outline-none max-sm:max-w-none max-sm:flex-1 max-sm:pl-2"
      title={session?.workspace}
      tabindex="-1"
    >
      {#if session}{title}
      {:else if gone}Thread unavailable
      {:else}<span class="sr-only">Loading thread</span><span
          class="{skeleton} inline-block w-40 text-transparent"
          aria-hidden="true">&nbsp;</span
        >{/if}
    </h1>
    {#if session}
      <Popover label="Session details" icon="info">
        {#snippet children(close)}
          <SessionInfo
            {session}
            {playback}
            {root}
            status={statusLabel}
            alert={!ended && !!stream.status.alert}
          />
          <div class="grid gap-2 px-4.5 pb-4">
            <p class="text-small text-muted">Continue in a terminal on {machine.label}</p>
            <div class="flex items-center gap-1 rounded-control bg-raised py-0.5 pr-0.5 pl-2.5">
              <code class="min-w-0 flex-1 text-small wrap-anywhere">mekugi resume {id}</code>
              <Button
                variant="quiet"
                icon={copied ? "check" : "copy"}
                label="Copy resume command"
                title={copied ? "Copied" : "Copy"}
                onclick={copyResume}
              />
            </div>
            {#if !ended}<Button
                variant="danger"
                onclick={() => {
                  close();
                  onend({ machine, id, title, replay: !!playback });
                }}>{playback ? "End replay" : "End thread"}</Button
              >{/if}
          </div>
        {/snippet}
      </Popover>
    {/if}
    <span class="contents max-sm:hidden">
      {#if playback}<Tag>Replay</Tag>{:else if live && !ended}<Tag tone="live">Live</Tag>{/if}
      <span class="flex-none text-small text-muted">{machine.label}</span>
    </span>
    <div class="ml-auto flex min-w-0 items-center justify-end gap-0.5">
      {#if session && failure}
        <button
          type="button"
          popovertarget={failureID}
          aria-expanded={failureOpen}
          aria-label="{headline}: show the error"
          title="Show the error"
          class="mr-1 inline-flex min-h-8 min-w-0 items-center gap-2 rounded-control px-2 text-small text-negative hover:bg-active aria-expanded:bg-active max-sm:mr-0 max-sm:min-h-11"
          ><Dot tone="alert" /><span class="truncate max-sm:hidden">{headline}</span><span
            class="flex flex-none"><Icon name="chevron" size={12} /></span
          ></button
        >
        <div
          id={failureID}
          class="fixed inset-auto top-[calc(var(--topbar)+env(safe-area-inset-top,0px)+6px)] right-2 m-0 max-h-[calc(100dvh-var(--topbar)-24px)] w-[min(480px,calc(100vw-24px))] overflow-y-auto p-0 {floating}"
          popover="auto"
          onbeforetoggle={(e) => (failureOpen = e.newState === "open")}
        >
          <p class="px-4 pt-3 font-semibold text-negative">{headline}</p>
          <p class="px-4 pt-1 pb-4 text-small whitespace-pre-wrap wrap-anywhere">{failure}</p>
        </div>
      {:else if session && statusLabel}
        <span
          class={[
            "mr-2 inline-flex min-w-0 items-center gap-2 text-small max-sm:mr-0.5",
            !ended && stream.status.alert
              ? "text-negative"
              : !ended && stream.status.running
                ? "text-accent"
                : "text-muted",
          ]}
          title={statusLabel}
          ><Dot
            tone={!ended && stream.status.alert
              ? "alert"
              : !ended && stream.status.running
                ? "running"
                : "idle"}
          /><span class="truncate max-sm:hidden">{statusLabel}</span></span
        >
      {/if}
      {#if session || !gone}
        <Button
          variant="quiet"
          icon="agents"
          label="Agents ({stream.roster.agents.length})"
          title="Agents"
          aria-pressed={panel === "agents"}
          onclick={() => toggle("agents")}
          ><span class="tabular-nums">{stream.roster.agents.length}</span></Button
        >
      {/if}
      {#if changes}
        <Button
          variant="quiet"
          icon="diff"
          label="Changes"
          title="Changes"
          aria-pressed={panel === "changes"}
          onclick={() => toggle("changes")}
          >{#if net && net.Added >= 0 && net.Removed >= 0 && (net.Added || net.Removed)}<span
              class="font-mono text-small tabular-nums max-sm:hidden"
              ><span class="text-positive">+{net.Added}</span>
              <span class="text-negative">−{net.Removed}</span></span
            >{/if}</Button
        >
      {/if}
    </div>
  </header>

  <div class="relative flex min-h-0">
    <main class="flex min-h-0 min-w-0 flex-1 flex-col">
      {#if opened}<Notice class="{column} mt-3">{opened}</Notice>{/if}
      {#if notice}
        <Notice class="{column} mt-3" role={stream.connection === "open" ? "alert" : "status"}
          >{notice}</Notice
        >
      {/if}

      {#if tabs && sview}
        <div class="{column} {segments} mt-2.5 flex" role="tablist" aria-label="Conversation">
          <button
            type="button"
            role="tab"
            class="{segment} flex-1"
            aria-selected={!side}
            disabled={semantic.pending > 0}
            onclick={() =>
              !side || semantic.send({ display: { Version: sview.Version, Thread: sview.Main } })}
            >Main</button
          >
          <span class="relative flex min-w-0 flex-1">
            <button
              type="button"
              role="tab"
              class="{segment} w-full pr-8"
              aria-selected={side}
              disabled={semantic.pending > 0 || !sview.Side}
              onclick={() =>
                side ||
                semantic.send({ display: { Version: sview.Version, Thread: sview.SideThread } })}
              >{sview.Side?.Label || "Side conversation"}</button
            >
            {#if sview.Side}<button
                type="button"
                class="absolute top-[3px] right-[3px] flex size-6 items-center justify-center rounded-chip text-muted hover:bg-active hover:text-ink"
                aria-label="Close side"
                title="Close side"
                disabled={semantic.pending > 0}
                onclick={() =>
                  semantic.send({
                    side: {
                      Version: sview.Version,
                      Key: sview.Side!.Key,
                      Exchange: sview.Side!.ID,
                    },
                  })}><Icon name="close" size={14} /></button
              >{/if}
          </span>
        </div>
      {/if}

      {#if shown}
        {@const time = worked(shown, now)}
        <div class="{column} mt-2.5 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-small">
          <Button size="sm" icon="left" label="Back to main" onclick={() => select("/root")}
            >main</Button
          >
          <span class="text-control" aria-hidden="true">/</span>
          <span class="text-body font-semibold">{shown.label}</span>
          {#if shown.role}<Tag>{shown.role}</Tag>{/if}
          <span
            class={shown.state === "working"
              ? "text-accent"
              : shown.state === "error"
                ? "text-negative"
                : "text-muted"}>{stateLabel[shown.state]}</span
          >
          {#if time !== undefined}<span class="font-mono text-muted tabular-nums"
              >{duration(time)}</span
            >{/if}
        </div>
      {/if}

      <section
        class="relative min-h-0 flex-1 focus:outline-none"
        tabindex="-1"
        bind:this={feed}
        aria-label={shown ? `${shown.label}'s activity` : "Transcript"}
      >
        {#if live && side && frame}
          <NativeTranscript
            publication={frame.main}
            label={sview?.Side?.Label || "Side conversation"}
          />
        {:else if session}
          {#key owner}
            <Transcript
              {entries}
              {owner}
              {start}
              span={playback?.until ?? 0}
              jumps={childFeedReady ? child.jumps : stream.jumps}
              {empty}
              {live}
              source={childFeedReady ? child : stream}
              ondetails={live && !ended && sview && (!childThread || childFeedReady)
                ? (seq) =>
                    semantic.send({
                      details: {
                        Displayed: sview.Displayed,
                        Kind: "entry",
                        Source: seq,
                        Output: 0,
                        Sources: [],
                        Agent: !!shown && !childFeedReady,
                        Path: "",
                        Caller: owner,
                      },
                    })
                : undefined}
            ></Transcript>
            {#if live && !ended && (!childThread || childFeedReady)}<LiveSource
                previews={active.live}
                {owner}
              />{/if}
          {/key}
        {:else if gone}
          <div class="mx-auto grid max-w-column gap-1.5 px-4 py-6">
            <p>{gone.text}</p>
            <p class="text-small text-muted">Choose another thread, or start a new one.</p>
          </div>
        {:else}
          <div class="mx-auto max-w-column px-4 py-6" aria-busy="true" aria-label="Loading thread">
            {#each [72, 48, 88, 36] as width, i (i)}
              <div class="{skeleton} mb-4 h-5" style:width="{width}%">&nbsp;</div>
            {/each}
          </div>
        {/if}
      </section>

      {#if playback}
        <div class="flex-none border-t border-rule">
          <PlaybackBar {playback} {start} entries={main} />
        </div>
      {:else if live && !ended}
        <div class="flex-none pt-1">
          {#if shown && !childThread}
            <p class="{column} py-3 text-small text-muted">
              This agent's retained activity is read-only.
            </p>
          {:else}
            {#if (!childThread || childFeedReady) && active.prompts && (active.prompts.approvals.length || active.prompts.questions.length)}
              <section
                class="mx-auto flex max-h-[50dvh] w-full max-w-column flex-col gap-3 overflow-y-auto overscroll-contain px-4 pb-2"
                aria-label="Pending prompts"
              >
                {#each active.prompts.approvals.slice(0, 1) as approval (approval.id)}
                  <Prompt
                    item={approval}
                    count={active.prompts.approvals.length}
                    unavailable={active.connection !== "open"}
                    source={active}
                  />
                {/each}
                {#each active.prompts.questions as question (question.id)}
                  <Prompt
                    item={question}
                    unavailable={active.connection !== "open"}
                    source={active}
                  />
                {/each}
              </section>
            {/if}
            <ThreadDocks />
            {#key childThread ?? id}
              <Composer
                input={!childThread || childFeedReady ? active.input : undefined}
                source={active}
                agentName={shown?.label ?? "main"}
                machine={machine.label}
                initial={shown ? undefined : initial}
                {oninitial}
                unavailable={(!shown || childFeedReady) &&
                active.connection === "open" &&
                semantic.connection === "open"
                  ? undefined
                  : "Not connected to the thread."}
              />
            {/key}
          {/if}
        </div>
      {/if}
    </main>

    {#if panel}
      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (Escape and the panel's close button also close it) -->
      <div
        class="hidden max-lg:absolute max-lg:inset-0 max-lg:z-9 max-lg:block max-lg:bg-scrim"
        onclick={closePanel}
      ></div>
      <!-- Beside the transcript where it fits; a sheet over it otherwise. -->
      <aside
        class={[
          "flex min-h-0 flex-none flex-col border-l border-rule bg-surface",
          panel === "agents"
            ? "w-side"
            : panel === "details"
              ? "w-[min(480px,40vw)]"
              : "w-[min(640px,46vw)]",
          "max-lg:absolute max-lg:inset-y-0 max-lg:right-0 max-lg:z-10 max-lg:w-[min(560px,92vw)] max-lg:border-border max-lg:shadow-float",
          "max-sm:w-full max-sm:border-l-0 max-sm:pb-safe",
        ]}
        aria-label="Thread panel"
      >
        <div class="flex min-h-12 flex-none items-center gap-2 border-b border-rule pr-1.5 pl-3">
          <div class="{segments} flex min-w-0" role="tablist" aria-label="Panel">
            {#snippet tabButton(target: Tab, label: string)}
              <button
                type="button"
                role="tab"
                class={segment}
                aria-selected={panel === target}
                aria-controls="panel-content"
                onclick={() => (chosen = target)}>{label}</button
              >
            {/snippet}
            {@render tabButton("agents", "Agents")}
            {#if changes}{@render tabButton("changes", "Changes")}{/if}
            {#if details}{@render tabButton("details", "Details")}{/if}
          </div>
          <Button
            variant="quiet"
            icon="close"
            label="Close panel"
            class="ml-auto"
            onclick={closePanel}
          />
        </div>
        <div id="panel-content" role="tabpanel" class="flex min-h-0 flex-1 flex-col">
          {#if panel === "details" && details}
            <DetailsPanel {details} onclose={closeDetails} />
          {:else if panel === "changes"}
            <div class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-3.5 pb-6">
              <DiffPanel review={stream.review} {owner} />
            </div>
          {:else}
            <!-- Positioned, so the overflow clips hidden labels, which are positioned. -->
            <div class="relative min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-1 pb-5">
              <Roster
                roster={stream.roster}
                lines={stream.review?.counts.Callers}
                net={stream.review?.counts.Net}
                loading={!session}
                selected={owner}
                {now}
                {activity}
                onselect={select}
              />
            </div>
          {/if}
        </div>
      </aside>
    {/if}
  </div>
  {#if live && !ended && (!shown || childFeedReady)}<SemanticDialogs />{/if}
</div>
