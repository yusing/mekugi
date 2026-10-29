<script lang="ts">
  import type { Entry } from "./protocol";
  import { stream, type Stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";

  // An image bound to an entry: one a user attached, or one an agent viewed.
  // label names it for assistive technology; small fits a row of thumbnails.
  let {
    entry,
    index,
    image,
    label = `Attached image ${index + 1}`,
    small = false,
    source = stream,
  }: {
    entry: Entry;
    index: number;
    image: string;
    label?: string;
    small?: boolean;
    source?: Stream;
  } = $props();
  const size = $derived(small ? "h-32 w-44" : "h-48 w-64");
  let element = $state<HTMLElement>();
  let url = $state<string>();
  let error = $state<string>();
  let retry = $state(0);
  // The read is keyed by the published image's identity, not the entry object,
  // which shared snapshots and window replacements recreate unchanged.
  const seq = $derived(entry.seq);
  const agent = $derived(entry.agent);
  const attachment = $derived(source.main.attachment);
  $effect(() => {
    if (!element) return;
    void attachment;
    const request = { seq, agent, index, image, retry, source };
    const controller = new AbortController();
    let objectURL: string | undefined;
    url = error = undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((e) => e.isIntersecting)) return;
        observer.disconnect();
        void request.source
          .image(request, request.index, request.image, controller.signal)
          .then((blob) => {
            if (!controller.signal.aborted) url = objectURL = URL.createObjectURL(blob);
          })
          .catch((failure) => {
            if (!controller.signal.aborted)
              error = failure instanceof Error ? failure.message : "Image unavailable.";
          });
      },
      { rootMargin: "200px" },
    );
    observer.observe(element);
    return () => {
      observer.disconnect();
      controller.abort();
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  });
</script>

<figure class="{size} min-w-0 max-w-full" bind:this={element}>
  {#if url}
    <a href={url} target="_blank" rel="noopener noreferrer" aria-label={`Open ${label}`}>
      <img
        src={url}
        alt={label}
        width={small ? 176 : 256}
        height={small ? 128 : 192}
        class={[size, "max-w-full rounded-card object-contain", small && "bg-raised"]}
        onerror={() => {
          error = "Image could not be decoded.";
          url = undefined;
        }}
      />
    </a>
  {:else}
    <div
      class="grid {small
        ? 'h-32'
        : 'h-48'} w-full place-content-center gap-1 rounded-card bg-raised px-3 py-2 text-small text-muted"
      role="status"
    >
      <span>{error ?? (small ? "Loading…" : `Loading image ${index + 1}…`)}</span>
      {#if error}<Button size="sm" variant="quiet" onclick={() => retry++}>Retry image</Button>{/if}
    </div>
  {/if}
</figure>
