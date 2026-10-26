<script lang="ts">
  import { breakable, highlight } from "./highlight";
  import Path from "./Path.svelte";

  // Code text, highlighted once its grammar has loaded. Until then, or when
  // lang names no known grammar, it is plain text. wrap lets paths break after
  // a separator for code shown inline.
  let { code, lang, wrap = false }: { code: string; lang?: string; wrap?: boolean } = $props();

  let result = $state<{ code: string; lang?: string; html?: string }>();
  $effect(() => {
    const request = { code, lang };
    let current = true;
    void highlight(request.code, request.lang).then((html) => {
      if (current) result = { ...request, html: html && wrap ? breakable(html) : html };
    });
    return () => (current = false);
  });
  const html = $derived(
    result && result.code === code && result.lang === lang ? result.html : undefined,
  );
</script>

{#if html}{@html html}{:else if wrap}<Path path={code} />{:else}{code}{/if}
