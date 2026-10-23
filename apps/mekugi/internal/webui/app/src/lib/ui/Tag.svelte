<script lang="ts" module>
  export type Tone = "neutral" | "live" | "negative";
</script>

<script lang="ts">
  import type { Snippet } from "svelte";

  // A short label beside a value: a pale tint with ink of the same hue.
  let {
    tone = "neutral",
    mono = false,
    class: extra = "",
    children,
  }: { tone?: Tone; mono?: boolean; class?: string; children: Snippet } = $props();
  const tones: Record<Tone, string> = {
    neutral: "bg-neutral-tint text-neutral-ink",
    live: "bg-accent-tint text-accent",
    negative: "bg-negative-tint text-negative-ink",
  };
</script>

<span
  class={[
    "inline-block flex-none rounded-chip px-[7px] text-tiny font-medium whitespace-nowrap",
    mono ? "font-mono" : "font-sans",
    tones[tone],
    extra,
  ]}>{@render children()}</span
>
