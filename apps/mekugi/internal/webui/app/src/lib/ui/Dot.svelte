<script lang="ts" module>
  export type Tone = "idle" | "open" | "running" | "done" | "alert" | "warn" | "hollow" | "none";
</script>

<script lang="ts">
  // A state carried by a dot's colour: a machine's connection, a thread's
  // activity or an agent's state. Its text alternative sits beside it.
  let { tone = "idle", size = 7 }: { tone?: Tone; size?: number } = $props();
  const tones: Record<Tone, string> = {
    idle: "bg-control",
    open: "bg-accent",
    running: "animate-blink bg-accent",
    done: "bg-positive",
    alert: "bg-negative",
    warn: "bg-warning-ink",
    hollow: "shadow-[inset_0_0_0_1.5px_var(--control)]",
    none: "",
  };
</script>

<span
  class="inline-block flex-none rounded-full {tones[tone]}"
  style:width="{size}px"
  style:height="{size}px"
  aria-hidden="true"
></span>
