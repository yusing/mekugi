<script lang="ts">
  import { highlightWithin } from "./highlight";
  import { block, markedBlock } from "./markdown";

  // Rendered Markdown from recorded messages, styled by the prose component
  // class since no template declares its elements.
  let {
    text,
    quiet = false,
    spans,
  }: { text: string; quiet?: boolean; spans?: import("./protocol").Entry["spans"] } = $props();
  const html = $derived(spans?.length ? markedBlock(text, spans) : block(text));
  // Fenced code names its language as a language-NAME class.
  const fences = (rendered: string) => (node: HTMLElement) => {
    if (rendered.includes("<pre>"))
      highlightWithin(node, "pre > code", (code) => /language-(\S+)/.exec(code.className)?.[1]);
  };
</script>

<div class={["prose", quiet && "text-muted"]} {@attach fences(html)}>{@html html}</div>
