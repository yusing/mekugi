<script lang="ts">
  import { offset, speed } from "./format";
  import Icon from "./ui/Icon.svelte";
  import { failed, family, isUser } from "./kinds";
  import type { Entry, Playback } from "./protocol";
  import { stream } from "./stream.svelte";

  let { playback, start, entries }: { playback: Playback; start: number; entries: Entry[] } =
    $props();

  let strip = $state<HTMLElement>();
  let lane = $state<HTMLElement>();
  let dragging = $state<number | undefined>();
  // A released drag's position, shown until playback reaches it, so the head
  // does not snap back while the seek is on its way.
  let seeking = $state<number | undefined>();
  let seekTimer: ReturnType<typeof setTimeout> | undefined;
  // The lane's box, measured once per drag so nothing that reflows during the
  // drag moves the scale under the pointer.
  let box: DOMRect | undefined;

  const ended = $derived(playback.position >= playback.until);
  const shown = $derived(dragging ?? seeking ?? playback.position);
  $effect(() => {
    if (seeking !== undefined && Math.abs(playback.position - seeking) <= 2000 * playback.speed)
      seeking = undefined;
  });
  const ratio = (ms: number) =>
    playback.until > 0 ? Math.min(1, Math.max(0, ms / playback.until)) : 0;

  // The clock strip: one mark per revealed entry at its recorded offset, in
  // the transcript's colours. Turns start at user messages.
  const marks = $derived(
    entries
      .filter((e) => e.observed)
      .map((e) => ({
        x: ratio(e.observed! - start),
        kind: isUser(e)
          ? "turn"
          : e.values?.some(failed)
            ? "failed"
            : e.kind === "text" || e.kind === "final"
              ? "text"
              : e.kind === "hook"
                ? "hook"
                : e.values?.some((v) => family(v.verb) === "write")
                  ? "write"
                  : "work",
      })),
  );

  const clock = (ms: number) => offset(ms, playback.until);

  function at(event: PointerEvent) {
    const { left, width } = box ?? lane!.getBoundingClientRect();
    return Math.round(ratio(((event.clientX - left) / width) * playback.until) * playback.until);
  }
  function down(event: PointerEvent) {
    strip!.setPointerCapture(event.pointerId);
    box = lane!.getBoundingClientRect();
    dragging = at(event);
  }
  function move(event: PointerEvent) {
    if (dragging !== undefined) dragging = at(event);
  }
  function up(event: PointerEvent) {
    if (dragging === undefined) return;
    const position = at(event);
    seeking = position;
    clearTimeout(seekTimer);
    seekTimer = setTimeout(() => (seeking = undefined), 2000);
    dragging = box = undefined;
    stream.control("seek", position);
  }
  // Arrows step ten seconds, as the buttons do; pages step a tenth of the recording.
  function key(event: KeyboardEvent) {
    const page = Math.max(10_000, playback.until / 10);
    if (event.key === "ArrowLeft") stream.control("back");
    else if (event.key === "ArrowRight") stream.control("forward");
    else if (event.key === "PageUp") stream.control("seek", Math.max(0, playback.position - page));
    else if (event.key === "PageDown")
      stream.control("seek", Math.min(playback.until, playback.position + page));
    else if (event.key === "Home") stream.control("seek", 0);
    else if (event.key === "End") stream.control("seek", playback.until);
    else if (event.key === " ") stream.control(ended ? "restart" : "toggle-pause");
    else return;
    event.preventDefault();
  }

  // Full-height touch targets on phones, narrowed so the hour-wide clock and
  // the speed controls share one row at 390px.
  const control =
    "grid h-8 w-8 place-items-center rounded-control text-muted hover:bg-active hover:text-ink max-sm:h-11";
  const marked: Record<string, string> = {
    work: "h-1.5 w-px bg-control",
    hook: "h-[3px] w-px bg-control",
    text: "h-2.5 w-px bg-muted",
    write: "h-2.5 w-px bg-kind-write",
    turn: "h-[18px] w-0.5 bg-ink",
    failed: "h-3 w-0.5 bg-negative",
  };
</script>

<div
  class="flex items-center gap-4 border-t px-4 pt-2.5 pb-safe-2.5 max-sm:flex-wrap max-sm:gap-x-1 max-sm:gap-y-0 max-sm:px-3 max-sm:pt-1 max-sm:pb-safe-2"
>
  <div class="flex flex-none items-center gap-0.5 max-sm:flex-1">
    <!-- Play is the screen's one primary action. -->
    <button
      type="button"
      class="mr-1.5 grid h-9 w-9 place-items-center rounded-full bg-primary text-on-primary hover:opacity-88 max-sm:mr-0 max-sm:h-11 max-sm:w-11"
      aria-label={ended ? "Restart" : playback.paused ? "Play" : "Pause"}
      title={ended ? "Restart (r)" : playback.paused ? "Play (space)" : "Pause (space)"}
      onclick={() => stream.control(ended ? "restart" : "toggle-pause")}
    >
      <Icon name={ended ? "restart" : playback.paused ? "play" : "pause"} />
    </button>
    <button
      type="button"
      class="{control} max-sm:w-10"
      aria-label="Back 10 seconds"
      title="Back 10s ([)"
      onclick={() => stream.control("back")}><Icon name="back" /></button
    >
    <button
      type="button"
      class="{control} max-sm:w-10"
      aria-label="Forward 10 seconds"
      title="Forward 10s (])"
      onclick={() => stream.control("forward")}><Icon name="forward" /></button
    >
    <span class="ml-2.5 font-mono text-small whitespace-nowrap tabular-nums max-sm:ml-1"
      ><span>{clock(shown)}</span><span class="text-muted">{` / ${clock(playback.until)}`}</span
      ></span
    >
  </div>

  <div
    class="relative h-9 min-w-[120px] flex-1 cursor-pointer touch-none rounded-chip max-sm:order-first max-sm:h-10 max-sm:basis-full"
    bind:this={strip}
    role="slider"
    tabindex="0"
    aria-label="Recorded position"
    aria-valuemin={0}
    aria-valuemax={Math.round(playback.until / 1000)}
    aria-valuenow={Math.round(shown / 1000)}
    aria-valuetext={`${clock(shown)} of ${clock(playback.until)}`}
    onpointerdown={down}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={() => (dragging = box = undefined)}
    onkeydown={key}
  >
    <!-- Inset so the head and the first and last marks stay inside the strip. -->
    <div class="absolute inset-x-[7px] inset-y-0" bind:this={lane}>
      <div class="absolute inset-x-0 bottom-2 h-1 overflow-hidden rounded-full bg-active">
        <span class="block h-full bg-accent" style:width="{ratio(shown) * 100}%"></span>
      </div>
      {#each marks as mark, i (i)}
        <span class="absolute bottom-[15px] {marked[mark.kind]}" style:left="{mark.x * 100}%"
        ></span>
      {/each}
      <span
        class="absolute bottom-1 -ml-1.5 h-3 w-3 rounded-full border-2 border-surface bg-accent shadow-[0_0_0_1px_var(--accent)]"
        style:left="{ratio(shown) * 100}%"
      ></span>
    </div>
  </div>

  <div class="flex flex-none items-center gap-0.5">
    <button
      type="button"
      class="{control} max-sm:w-7"
      aria-label="Slower"
      title="Slower (-)"
      onclick={() => stream.control("slower")}><Icon name="minus" /></button
    >
    <span class="min-w-10 text-center font-mono text-small tabular-nums max-sm:min-w-[34px]"
      >{speed(playback.speed)}</span
    >
    <button
      type="button"
      class="{control} max-sm:w-7"
      aria-label="Faster"
      title="Faster (+)"
      onclick={() => stream.control("faster")}><Icon name="plus" /></button
    >
  </div>
  {#if playback.simulated}<span
      class="text-small whitespace-nowrap text-muted max-lg:hidden"
      title="Replay reveals recorded text on the recorded clock">Simulated streaming</span
    >{/if}
</div>
