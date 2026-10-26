<script lang="ts">
  import { untrack } from "svelte";

  let {
    cursor,
    ready = true,
    onpage,
  }: { cursor: string; ready?: boolean; onpage: (signal: AbortSignal) => unknown } = $props();
  let visible = $state(false);
  let requested = "";
  let request: AbortController | undefined;
  let marker = $state<HTMLElement>();

  $effect(() => {
    void cursor;
    if (!marker) return;
    visible = false;
    const observer = new IntersectionObserver(([entry]) => (visible = entry.isIntersecting));
    observer.observe(marker);
    return () => observer.disconnect();
  });
  $effect(() => {
    const shown = visible,
      key = cursor;
    untrack(() => {
      if (!shown || requested !== key) {
        request?.abort();
        requested = "";
      }
    });
  });
  $effect(() => () => request?.abort());
  $effect(() => {
    if (!visible || !ready || requested === cursor) return;
    requested = cursor;
    request = new AbortController();
    untrack(() => onpage(request!.signal));
  });
</script>

<span class="block h-px" aria-hidden="true" bind:this={marker}></span>
