<script lang="ts">
  import { highlightWithin } from "./highlight";
  import { block } from "./markdown";

  // Rendered Markdown from recorded messages, styled by the prose component
  // class since no template declares its elements.
  let { text, quiet = false }: { text: string; quiet?: boolean } = $props();
  const html = $derived(block(text));
  // Fenced code names its language as a language-NAME class.
  const fences = (rendered: string) => (node: HTMLElement) => {
    if (rendered.includes("<pre>"))
      highlightWithin(node, "pre > code", (code) => /language-(\S+)/.exec(code.className)?.[1]);
  };
</script>

<div class={["prose", quiet && "text-muted"]} {@attach fences(html)}>{@html html}</div>
