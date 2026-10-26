<script lang="ts">
  import Markdown from "./Markdown.svelte";
  import { caretButton } from "./ui/styles";

  // A reply or task shown away from its own agent's transcript: its first
  // lines, with the rest one click away. noun names what the rest completes.
  let { text, noun = "reply" }: { text: string; noun?: string } = $props();

  // Five lines of prose; the text only clamps when that hides two more lines.
  const clamp = 110;
  const slack = 44;
  let open = $state(false);
  let long = $state(false);
  // Measures the whole reply, which keeps its height while the excerpt is
  // clamped, so streamed growth is noticed either way.
  const measure = (node: HTMLElement) => {
    const reply = node.firstElementChild as HTMLElement;
    const observer = new ResizeObserver(() => (long = reply.offsetHeight > clamp + slack));
    observer.observe(reply);
    return () => observer.disconnect();
  };
</script>

<!-- Clipped content cannot be scrolled into view by focus; focus opens it.
     The fade ends before the cut, so no partial line shows. -->
<div
  class={long && !open
    ? "overflow-clip mask-[linear-gradient(to_bottom,#000_45%,transparent_85%)]"
    : undefined}
  style:max-height={long && !open ? `${clamp}px` : undefined}
  onfocusin={() => (open = true)}
  {@attach measure}
>
  <Markdown {text} />
</div>
{#if long}
  <!-- The same marker as other disclosures, pointing down while closed. -->
  <button
    type="button"
    class="-ml-1 inline-flex min-h-[26px] items-center gap-1.5 rounded-chip px-1 text-small text-muted hover:text-ink pointer-coarse:min-h-9 {caretButton}"
    aria-expanded={open}
    onclick={() => (open = !open)}
  >
    {open ? "Show less" : `Show full ${noun}`}
  </button>
{/if}
