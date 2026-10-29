<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import EntryView from "./EntryView.svelte";
  import { date, offset } from "./format";
  import { hasDetails, isUser, isView, responseText } from "./kinds";
  import ResponseCopy from "./ResponseCopy.svelte";
  import type { Entry } from "./protocol";
  import { stream, type Stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";
  import Alert from "./ui/Alert.svelte";
  import { errorText } from "./transport";
  import Icon from "./ui/Icon.svelte";
  import { emptyBox, revealed, rowAction } from "./ui/styles";

  // entries are one agent's activity, owner's; span is the recorded length,
  // so offsets keep one width; jumps counts seeks and fresh snapshots, each of
  // which returns the view to the end. A live session shows clock times
  // instead of recorded offsets. A live session's rows offer their details;
  // header leads the transcript, above its oldest row.
  let {
    entries,
    owner,
    caller,
    start,
    span,
    jumps,
    empty,
    live = false,
    ondetails,
    header,
    source = stream,
  }: {
    entries: Entry[];
    owner: string;
    caller?: string;
    start: number;
    span: number;
    jumps: number;
    empty: string;
    live?: boolean;
    ondetails?: (seq: number) => unknown;
    header?: Snippet;
    source?: Stream;
  } = $props();

  let scroller = $state<HTMLElement>();
  let column = $state<HTMLElement>();
  let clipped = $state<Record<number, boolean>>({});
  let following = $state(true);
  // Whether newer rows lie below the visible part of the feed.
  let below = $state(false);
  // Set by the reader's own input, so the scrolls this component makes and
  // late font or layout growth never read as scrolling away.
  let reading = false;

  // Offsets print once per recorded second, clock times once a minute, and
  // user messages count turns. Images one agent views in a row share a row,
  // as do its consecutive thoughts.
  const rows = $derived.by(() => {
    let previous = "";
    const out: {
      entry: Entry;
      at: string;
      turn?: number;
      views?: Entry[];
      thoughts?: Entry[];
    }[] = [];
    for (const entry of entries) {
      const last = out.at(-1);
      if (last?.thoughts && entry.kind === "reasoning" && entry.agent === last.entry.agent) {
        last.thoughts.push(entry);
        continue;
      }
      if (
        last?.views &&
        isView(entry) &&
        entry.agent === last.entry.agent &&
        entry.values?.[0]?.verb === last.entry.values?.[0]?.verb
      ) {
        last.views.push(entry);
        continue;
      }
      const at = !entry.observed
        ? ""
        : live
          ? date(entry.observed).slice(11)
          : offset(entry.observed - start, span);
      const user = isUser(entry);
      out.push({
        entry,
        at: user || at !== previous ? at : "",
        turn: entry.turn,
        views: isView(entry) ? [entry] : undefined,
        thoughts: entry.kind === "reasoning" ? [entry] : undefined,
      });
      previous = at;
    }
    return out;
  });

  // Keep initial Markdown, code highlighting and DOM work proportional to a
  // page, while retaining every earlier row for explicit reading.
  const pageSize = 50;
  let oldest = $state<number>();
  const first = $derived(
    oldest === undefined
      ? Math.max(0, rows.length - pageSize)
      : Math.max(
          0,
          rows.findIndex((row) => row.entry.seq === oldest),
        ),
  );
  const visible = $derived(rows.slice(first));
  $effect(() => {
    if (following) oldest = rows[Math.max(0, rows.length - pageSize)]?.entry.seq;
  });
  let paging = false;
  const canEarlier = $derived(first > 0 || !!source.history(caller).earlier);
  const pageError = $derived(source.historyErrors[caller ?? ""]);
  async function earlier() {
    if (!scroller || !column || paging || !canEarlier) return;
    paging = true;
    following = false;
    const container = scroller;
    const anchor = column.querySelector<HTMLElement>("[data-entry]");
    const top = anchor?.getBoundingClientRect().top ?? 0;
    if (first === 0) {
      await source.earlier(caller, reads.signal);
      await tick();
    }
    oldest = rows[Math.max(0, first - pageSize)]?.entry.seq;
    await tick();
    if (anchor && scroller === container)
      container.scrollTop += anchor.getBoundingClientRect().top - top;
    paging = false;
    measure();
  }

  function pin() {
    if (!scroller) return;
    reading = false;
    scroller.scrollTop = scroller.scrollHeight;
  }

  // Follow the newest entry only while the reader is at the end; scrolling up
  // to read stops following until they return or jump.
  function measure() {
    if (scroller) below = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight >= 80;
  }
  function onscroll() {
    measure();
    if (!scroller || !reading) return;
    following = !below;
    if (
      scroller.scrollTop < scroller.scrollHeight - scroller.clientHeight &&
      scroller.scrollTop <= scroller.clientHeight &&
      canEarlier &&
      !pageError
    )
      void earlier();
  }
  const read = () => (reading = true);
  const scrollKeys = new Set(["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End"]);
  function onkeydown(event: KeyboardEvent) {
    if (scrollKeys.has(event.key) && !(event.target as HTMLElement).closest("[role=slider]"))
      read();
  }
  // Opening a disclosure or pressing a button, or moving focus into the
  // transcript, holds the view where it is, so the control under the pointer
  // or the focus stays in sight. Some browsers do not focus a clicked button.
  function hold() {
    following = false;
  }
  function onclick(event: MouseEvent) {
    if ((event.target as HTMLElement).closest("summary, button")) hold();
  }

  $effect(() => {
    void jumps;
    following = true;
    oldest = untrack(() => rows[Math.max(0, rows.length - pageSize)]?.entry.seq);
  });

  $effect(() => {
    void rows;
    if (following) tick().then(pin);
  });

  $effect(() => {
    if (!column) return;
    const observer = new ResizeObserver(() => {
      if (following) pin();
      measure();
    });
    observer.observe(column);
    return () => observer.disconnect();
  });

  function latest() {
    following = true;
    reading = false;
    scroller?.scrollTo({ top: scroller.scrollHeight, behavior: "smooth" });
  }

  let complete = $state.raw<Record<number, { reference: Entry["content"]; entry: Entry }>>({});
  let contentErrors = $state.raw<
    Record<number, { reference: Entry["content"]; text: string } | undefined>
  >({});
  let loading = $state.raw<Record<number, boolean>>({});
  const pending = new Map<
    number,
    { reference: Entry["content"]; read: AbortController; promise: Promise<Entry> }
  >();
  const reads = new AbortController();
  let autoPosition: number | undefined;
  $effect(() => () => reads.abort());
  $effect(() => {
    const current = new Map(entries.map((entry) => [entry.seq, entry.content]));
    untrack(() => {
      for (const [seq, request] of pending) {
        const reference = current.get(seq);
        if (
          reference?.attachment !== request.reference?.attachment ||
          reference?.revision !== request.reference?.revision
        )
          request.read.abort();
      }
      complete = Object.fromEntries(
        Object.entries(complete).filter(([seq]) => current.has(Number(seq))),
      );
    });
  });
  const historyIdentity = $derived(`${source.agents.attachment}:${source.agents.revision}`);
  $effect(() => {
    void historyIdentity;
    const selected = caller;
    if (!selected) return;
    const request = new AbortController();
    untrack(() => void source.prepareHistory(selected, request.signal));
    return () => request.abort();
  });
  $effect(() => {
    const changed = visible
      .flatMap((row) => row.thoughts ?? [row.entry])
      .filter((entry) => {
        const prior = complete[entry.seq]?.reference;
        return (
          entry.content &&
          (entry.autoContent || prior?.attachment === entry.content.attachment) &&
          (prior?.attachment !== entry.content.attachment ||
            prior?.revision !== entry.content.revision) &&
          !loading[entry.seq] &&
          !(
            contentErrors[entry.seq]?.reference?.attachment === entry.content.attachment &&
            contentErrors[entry.seq]?.reference?.revision === entry.content.revision
          )
        );
      });
    untrack(() => {
      if (Object.values(loading).some(Boolean)) return;
      const entry =
        autoPosition === undefined
          ? changed.at(-1)
          : (changed.find((candidate) => (candidate.position ?? candidate.seq) > autoPosition!) ??
            changed[0]);
      if (entry) {
        autoPosition = entry.position ?? entry.seq;
        void load(entry).catch(() => {});
      }
    });
  });
  // A revision keeps the attachment's acquired content readable until its
  // replacement is admitted. Apply this to every member of a thought group.
  function resolved(entry: Entry): Entry {
    const hydrated = complete[entry.seq];
    return entry.content && hydrated?.reference?.attachment === entry.content.attachment
      ? hydrated.entry
      : entry;
  }
  function load(entry: Entry): Promise<Entry> {
    const cached = complete[entry.seq];
    if (!entry.content) return Promise.resolve(entry);
    if (
      cached?.reference?.attachment === entry.content.attachment &&
      cached.reference.revision === entry.content.revision
    )
      return Promise.resolve(cached.entry);
    const active = pending.get(entry.seq);
    if (
      active?.reference?.attachment === entry.content.attachment &&
      active.reference.revision === entry.content.revision
    )
      return active.promise;
    active?.read.abort();
    const request = new AbortController();
    const promise = acquire(entry, AbortSignal.any([request.signal, reads.signal]));
    pending.set(entry.seq, { reference: entry.content, read: request, promise });
    return promise;
  }
  async function acquire(entry: Entry, signal: AbortSignal): Promise<Entry> {
    loading = { ...loading, [entry.seq]: true };
    contentErrors = { ...contentErrors, [entry.seq]: undefined };
    try {
      const result = await source.content(entry, signal);
      const current = entries.find((value) => value.seq === entry.seq)?.content;
      if (
        !signal.aborted &&
        current?.attachment === entry.content?.attachment &&
        current?.revision === entry.content?.revision
      )
        complete = { ...complete, [entry.seq]: { reference: entry.content, entry: result } };
      return result;
    } catch (error) {
      if (
        !signal.aborted &&
        entries.find((value) => value.seq === entry.seq)?.content?.attachment ===
          entry.content?.attachment &&
        entries.find((value) => value.seq === entry.seq)?.content?.revision ===
          entry.content?.revision
      )
        contentErrors = {
          ...contentErrors,
          [entry.seq]: { reference: entry.content, text: errorText(error) },
        };
      throw error;
    } finally {
      if (pending.get(entry.seq)?.reference === entry.content) {
        pending.delete(entry.seq);
        if (!reads.signal.aborted) loading = { ...loading, [entry.seq]: false };
      }
    }
  }
</script>

<svelte:window {onkeydown} onwheel={read} ontouchstart={read} onpointerdown={read} />

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (a summary handles its own keys; focus moving in holds the view) -->
<div
  class="@container h-full overflow-y-auto overscroll-contain"
  bind:this={scroller}
  {onscroll}
  {onclick}
  onfocusin={hold}
  aria-busy={!!source.historyLoading[caller ?? ""]}
>
  <!-- The rows' text shares the composer's column; recorded times sit in the
       gutter to its left, and each row's details action to its right. -->
  <div
    class="mx-auto max-w-[calc(var(--container-column)+184px)] pt-3 pr-23 pb-10 pl-4 @max-[980px]:pr-11 max-sm:pt-2 max-sm:pb-6"
    bind:this={column}
  >
    {@render header?.()}
    {#if pageError}
      <Alert
        >{pageError}<Button variant="quiet" onclick={earlier}>Retry earlier entries</Button></Alert
      >
    {:else if entries.length && !canEarlier && source.history(caller).loaded?.length}
      <p class="ml-19 text-small text-muted">Beginning of retained history</p>
    {/if}
    {#each visible as row (row.entry.seq)}
      {@const entry = resolved(row.entry)}
      {@const details = !!ondetails && hasDetails(entry, owner, clipped[entry.seq])}
      {@const response =
        !isUser(entry) && !entry.assignment
          ? responseText(entry.kind, entry.text, entry.values?.[0]?.body)
          : ""}
      <div class="group/row relative" data-entry={row.entry.seq}>
        <EntryView
          {entry}
          at={row.at}
          stamp={live ? "Time" : "Recorded offset"}
          turn={row.turn}
          views={row.views}
          thoughts={row.thoughts?.map(resolved)}
          {owner}
          {source}
          onload={() => void load(row.entry).catch(() => {})}
          onclip={ondetails
            ? (value) => {
                if (value) clipped[entry.seq] = true;
                else delete clipped[entry.seq];
              }
            : undefined}
        />
        {#each row.thoughts ?? [row.entry] as member (member.seq)}
          {#if !row.thoughts && loading[member.seq] && resolved(member) === member}<p
              class="ml-19 text-small text-muted"
              role="status"
            >
              Loading content…
            </p>{/if}
          {#if contentErrors[member.seq]?.reference?.attachment === member.content?.attachment && contentErrors[member.seq]?.reference?.revision === member.content?.revision && contentErrors[member.seq]}<Alert
              >{contentErrors[member.seq]!.text}<Button
                variant="quiet"
                onclick={() => void load(member).catch(() => {})}>Retry</Button
              ></Alert
            >{/if}
        {/each}
        <!-- The row's actions stack in the gutter, shown on hover or focus and
             always on touch. -->
        {#if details}<button
            type="button"
            class="{rowAction} {revealed} absolute top-1 -right-9 text-muted max-sm:-right-10"
            aria-label="Details"
            title="Details"
            onclick={() => ondetails?.(row.entry.seq)}><Icon name="expand" size={14} /></button
          >{/if}
        {#if response || (entry.content && !entry.assignment && !isUser(entry) && ["text", "final", "reply"].includes(entry.kind ?? ""))}<ResponseCopy
            text={response}
            gutter
            below={details}
            resolveText={entry.content
              ? async () => {
                  const full = await load(row.entry);
                  return responseText(full.kind, full.text, full.values?.[0]?.body);
                }
              : undefined}
          />{/if}
      </div>
    {:else}
      <p class="{emptyBox} mt-6 ml-17 max-sm:ml-0">{empty}</p>
    {/each}
  </div>
</div>
{#if !following && below}
  <button
    class="absolute bottom-4 left-1/2 flex h-8 -translate-x-1/2 items-center gap-1.5 rounded-full border bg-surface px-3.5 shadow-float hover:border-control max-sm:h-10"
    onclick={latest}><Icon name="down" size={14} /> Latest</button
  >
{/if}
