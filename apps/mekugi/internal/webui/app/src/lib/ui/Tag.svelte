<script lang="ts" module>
  export type Tone = "neutral" | "live" | "negative";
</script>

<script lang="ts">
  import type { Snippet } from "svelte";

  // A short label beside a value: a pale tint with ink of the same hue.
  // color gives a neutral tag the ink of an agent role.
  let {
    tone = "neutral",
    mono = false,
    color,
    class: extra = "",
    children,
  }: {
    tone?: Tone;
    mono?: boolean;
    color?: string;
    class?: string;
    children: Snippet;
  } = $props();
  const tones: Record<Tone, string> = {
    neutral: "bg-neutral-tint text-neutral-ink",
    live: "bg-accent-tint text-accent-ink",
    negative: "bg-negative-tint text-negative-ink",
  };
</script>

<span
  class={[
    "inline-block flex-none rounded-chip px-[7px] text-tiny font-medium whitespace-nowrap",
    mono ? "font-mono" : "font-sans",
    color ? "bg-neutral-tint text-role" : tones[tone],
    extra,
  ]}
  style:--role={color}>{@render children()}</span
>
