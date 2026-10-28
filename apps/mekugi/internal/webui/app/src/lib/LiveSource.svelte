<script lang="ts">
  import { untrack } from "svelte";
  import { workspacePath } from "./diff";
  import { basename } from "./format";
  import DiffView from "./DiffView.svelte";
  import AgentName from "./AgentName.svelte";
  import type { Preview } from "./protocol";
  import Button from "./ui/Button.svelte";
  import Dot from "./ui/Dot.svelte";
  import Tag from "./ui/Tag.svelte";

  // Edits owner is writing, shown over its transcript as the terminal shows
  // live source. Each completed call stays for its own settling interval; the
  // transcript returns when no cards remain. Calls already complete when the
  // view opens are history, not arrivals.
  let { previews, owner }: { previews: Preview[]; owner: string } = $props();

  // The terminal's completion hold.
  const hold = 1500;
  interface Card {
    preview: Preview;
    until?: number;
  }
  let cards = $state.raw<Card[]>([]);
  // Calls already shown or passed over; the server keeps completed calls
  // until their turn ends, and they must not settle again.
  const seen = new Set<string>();
  let primed = false;
  $effect(() => {
    const mine = previews.filter(
      (p) =>
        (owner === "/root" || p.Caller === owner) && (p.Files.length > 0 || p.Input.trim() !== ""),
    );
    untrack(() => {
      const now = Date.now();
      const current = new Map(mine.map((p) => [p.ID, p]));
      const next: Card[] = [];
      for (const card of cards) {
        const preview = current.get(card.preview.ID);
        current.delete(card.preview.ID);
        // A withdrawn call settles from its last content; a repeated
        // completion cannot restart its hold.
        const until = preview && !preview.Complete ? undefined : (card.until ?? now + hold);
        next.push({ preview: preview ?? card.preview, until });
      }
      for (const preview of current.values())
        if (!preview.Complete || (primed && !seen.has(preview.ID)))
          next.push({ preview, until: preview.Complete ? now + hold : undefined });
      for (const preview of mine) seen.add(preview.ID);
      primed = true;
      cards = next.filter((card) => card.until === undefined || card.until > now);
    });
  });
  $effect(() => {
    const ends = cards.flatMap((card) => (card.until ? [card.until] : []));
    if (!ends.length) return;
    const timer = setTimeout(
      () => (cards = cards.filter((card) => !card.until || card.until > Date.now())),
      Math.max(0, Math.min(...ends) - Date.now()) + 20,
    );
    return () => clearTimeout(timer);
  });

  // Hiding returns the transcript until another call arrives.
  let hidden = $state<string[]>([]);
  const shown = $derived(cards.filter((card) => !hidden.includes(card.preview.ID)));
  const writing = $derived(shown.some((card) => !card.preview.Complete));

  // Each code block follows independently until its reader scrolls away.
  function follow(scroller: HTMLElement) {
    let following = true;
    let end = 0;
    const scroll = () => {
      // Layout can clamp the scroll position before ResizeObserver runs.
      if (end === scroller.scrollHeight - scroller.clientHeight)
        following = end - scroller.scrollTop < 40;
    };
    const observer = new ResizeObserver(() => {
      end = scroller.scrollHeight - scroller.clientHeight;
      if (following) scroller.scrollTop = scroller.scrollHeight;
    });
    observer.observe(scroller);
    observer.observe(scroller.firstElementChild!);
    scroller.addEventListener("scroll", scroll);
    return {
      destroy() {
        observer.disconnect();
        scroller.removeEventListener("scroll", scroll);
      },
    };
  }
  const lines = (text: string) => text.replace(/\n$/, "").split("\n");
</script>

{#if shown.length}
  <section
    class="@container absolute inset-0 z-5 flex flex-col overflow-hidden bg-surface"
    aria-label="Live edits"
  >
    <div class="mx-auto flex w-[calc(100%-32px)] flex-none items-center gap-2 pt-3 pb-2">
      <h2 class="flex items-center gap-2 font-semibold">
        <Dot tone={writing ? "running" : "done"} size={6} />Live
      </h2>
      <span class="text-small text-muted">{writing ? "Writing" : "Settling"}; not saved yet.</span>
      <Button
        size="sm"
        variant="quiet"
        class="ml-auto"
        onclick={() => (hidden = cards.map((card) => card.preview.ID))}>Show transcript</Button
      >
    </div>
    <div class="min-h-0 flex-1 overflow-hidden">
      <div
        class="mx-auto grid h-full w-[calc(100%-32px)] auto-rows-fr grid-cols-1 gap-3 pb-6 @min-[40rem]:not-has-[>article:only-child]:grid-cols-2"
      >
        {#each shown as { preview } (preview.ID)}
          <article class="flex min-h-0 min-w-0 flex-col overflow-hidden pt-2 pb-2.5 wrap-anywhere">
            <div class="flex flex-none flex-wrap items-center gap-x-2 gap-y-1 text-small">
              <AgentName name={preview.Caller} current={owner} />
              {#if preview.Tool}<Tag mono>{preview.Tool}</Tag>{/if}
              {#if preview.Status.startsWith("unavailable: ")}<span class="text-muted"
                  >{preview.Status.slice(13)}</span
                >{/if}
              <span
                class={[
                  "ml-auto inline-flex items-center gap-1.5",
                  preview.Complete ? "text-muted" : "text-accent",
                ]}
                >{#if !preview.Complete}<Dot tone="running" size={6} />{/if}{preview.Complete
                  ? "done"
                  : "writing"}</span
              >
            </div>
            {#each preview.Files as file, i (i)}
              {@const path = workspacePath(file.AfterPath || file.BeforePath, preview.Workspace)}
              {@const name = basename(path)}
              <div class="flex min-h-0 flex-1 flex-col">
                <h3
                  class="mt-2 flex min-w-0 flex-none font-mono text-code whitespace-nowrap"
                  title={path}
                >
                  {#if path !== name}<span class="truncate">{path.slice(0, -name.length)}</span
                    >{/if}<span class="max-w-full shrink-0 truncate">{name}</span>
                </h3>
                <div class="min-h-0 flex-1 overflow-y-auto overscroll-contain" use:follow>
                  <DiffView {file} workspace={preview.Workspace} wrap />
                </div>
              </div>
            {:else}
              <div class="min-h-0 flex-1 overflow-y-auto overscroll-contain" use:follow>
                <div>
                  {#if preview.DiffText}
                    <DiffView
                      file={{ BeforePath: "", AfterPath: "", Diff: preview.Input }}
                      workspace={preview.Workspace}
                      wrap
                    />
                  {:else}
                    <pre
                      class="mt-1.5 rounded-card border py-1 font-mono text-code">{#each lines(preview.Input) as line, i (i)}<span
                        class="flex"
                        ><span class="w-[5ch] flex-none pr-2 text-right text-muted select-none"
                          >{i + 1}</span
                        ><span class="min-w-0 pr-3 whitespace-pre-wrap wrap-anywhere">{line}</span
                        ></span
                      >{/each}</pre>
                  {/if}
                </div>
              </div>
            {/each}
            {#if preview.Truncated}<p class="mt-1.5 flex-none text-small text-muted">
                Only the start of this edit is shown.
              </p>{/if}
            {#if preview.Footer}<p class="mt-1.5 flex-none text-small text-muted">
                {preview.Footer}
              </p>{/if}
          </article>
        {/each}
      </div>
    </div>
  </section>
{/if}
