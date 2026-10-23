<script lang="ts">
  import type { Entry } from "./protocol";
  import { stream } from "./stream.svelte";
  import Button from "./ui/Button.svelte";

  let { entry, index, image }: { entry: Entry; index: number; image: string } = $props();
  let element = $state<HTMLElement>();
  let url = $state<string>();
  let error = $state<string>();
  let retry = $state(0);
  $effect(() => {
    if (!element) return;
    const request = { entry, index, image, retry };
    const controller = new AbortController();
    let objectURL: string | undefined;
    url = error = undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((e) => e.isIntersecting)) return;
        observer.disconnect();
        void stream
          .image(request.entry, request.index, request.image, controller.signal)
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

<figure class="h-48 w-64 min-w-0 max-w-full" bind:this={element}>
  {#if url}
    <a href={url} target="_blank" rel="noopener noreferrer" aria-label={`Open image ${index + 1}`}>
      <img
        src={url}
        alt={`Attached image ${index + 1}`}
        width="256"
        height="192"
        class="h-48 w-64 max-w-full rounded-card object-contain"
        onerror={() => {
          error = "Image could not be decoded.";
          url = undefined;
        }}
      />
    </a>
  {:else}
    <div
      class="grid h-48 w-full place-content-center gap-1 rounded-card bg-raised px-3 py-2 text-small text-muted"
      role="status"
    >
      <span>{error ?? `Loading image ${index + 1}…`}</span>
      {#if error}<Button size="sm" variant="quiet" onclick={() => retry++}>Retry image</Button>{/if}
    </div>
  {/if}
</figure>
