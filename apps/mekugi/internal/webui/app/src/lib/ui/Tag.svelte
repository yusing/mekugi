<script lang="ts" module>
  export type Tone = "neutral" | "live" | "negative";
</script>

<script lang="ts">
  import { Badge } from "./badge";
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
  const tones = { neutral: "default", live: "live", negative: "destructive" } as const;
</script>

<Badge
  variant={color ? "role" : tones[tone]}
  class={[mono ? "font-mono" : "font-sans", extra]}
  style={color ? `--role:${color}` : undefined}>{@render children()}</Badge
>
