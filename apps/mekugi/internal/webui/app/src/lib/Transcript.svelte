<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import EntryView from "./EntryView.svelte";
  import { date, offset } from "./format";
  import { hasDetails, isUser, isView } from "./kinds";
  import type { Entry } from "./protocol";
  import { stream, type Stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";
  import Icon from "./ui/Icon.svelte";
  import { emptyBox } from "./ui/styles";

  // entries are one agent's activity, owner's; span is the recorded length,
  // so offsets keep one width; jumps counts seeks and fresh snapshots, each of
  // which returns the view to the end. A live session shows clock times
  // instead of recorded offsets. A live session's rows offer their details;
  // header leads the transcript, above its oldest row.
  let {
    entries,
    owner,
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
  let following = $state(true);
  // Whether newer rows lie below the visible part of the feed.
  let below = $state(false);
  // Set by the reader's own input, so the scrolls this component makes and
  // late font or layout growth never read as scrolling away.
  let reading = false;

  // Offsets print once per recorded second, clock times once a minute, and
  // user messages count turns. Images one agent views in a row share a row.
  const rows = $derived.by(() => {
    let previous = "";
    let turns = 0;
    const out: { entry: Entry; at: string; turn?: number; views?: Entry[] }[] = [];
    for (const entry of entries) {
      const last = out.at(-1);
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
        turn: user ? ++turns : undefined,
        views: isView(entry) ? [entry] : undefined,
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
  async function earlier() {
    if (!scroller || !column) return;
    following = false;
    const anchor = column.querySelector<HTMLElement>("[data-entry]");
    const top = anchor?.getBoundingClientRect().top ?? 0;
    oldest = rows[Math.max(0, first - pageSize)]?.entry.seq;
    await tick();
    if (anchor) {
      scroller.scrollTop += anchor.getBoundingClientRect().top - top;
      anchor.focus({ preventScroll: true });
      // Focus stays on the row being read, which takes focus only for this, so
      // clicking a row's text neither focuses nor rings it.
      anchor.tabIndex = -1;
      anchor.addEventListener("blur", () => anchor.removeAttribute("tabindex"), { once: true });
    }
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
</script>

<svelte:window {onkeydown} onwheel={read} ontouchstart={read} onpointerdown={read} />

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions (a summary handles its own keys; focus moving in holds the view) -->
<div
  class="@container h-full overflow-y-auto overscroll-contain"
  bind:this={scroller}
  {onscroll}
  {onclick}
  onfocusin={hold}
>
  <!-- The rows' text shares the composer's column; recorded times sit in the
       gutter to its left, and each row's details action to its right. -->
  <div
    class={[
      "mx-auto max-w-[calc(var(--container-column)+184px)] pt-3 pr-23 pb-10 pl-4 max-sm:pt-2 max-sm:pb-6",
      ondetails ? "@max-[980px]:pr-11" : "@max-[980px]:pr-4",
    ]}
    bind:this={column}
  >
    {@render header?.()}
    {#if first > 0}
      <!-- Centred on the rows' text, past the time gutter. -->
      <div class="mb-3 ml-19 flex justify-center max-sm:ml-0">
        <Button onclick={earlier}
          >Show {Math.min(first, pageSize)} earlier entries ({first} remaining)</Button
        >
      </div>
    {/if}
    {#each visible as row (row.entry.seq)}
      <div class="group/row relative" data-entry={row.entry.seq}>
        <EntryView
          entry={row.entry}
          at={row.at}
          stamp={live ? "Time" : "Recorded offset"}
          turn={row.turn}
          views={row.views}
          {owner}
          {source}
        />
        <!-- Shown on hover or focus, and always on touch. -->
        {#if ondetails && hasDetails(row.entry)}<button
            type="button"
            class="absolute top-1 -right-9 flex size-7 items-center justify-center rounded-control border border-transparent text-muted opacity-0 group-hover/row:opacity-100 hover:border-border hover:bg-surface hover:text-ink focus-visible:opacity-100 touch:opacity-100 max-sm:-right-10 max-sm:size-9"
            aria-label="Details"
            title="Details"
            onclick={() => ondetails?.(row.entry.seq)}><Icon name="expand" size={14} /></button
          >{/if}
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
