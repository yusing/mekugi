<script lang="ts" module>
  export type Tone = "idle" | "open" | "running" | "done" | "alert" | "warn" | "hollow" | "none";
</script>

<script lang="ts">
  // A state carried by a dot's colour: a machine's connection, a thread's
  // activity or an agent's state. Its text alternative sits beside it. color,
  // an agent role's, replaces the state's hue; the shape still says the state.
  let { tone = "idle", size = 7, color }: { tone?: Tone; size?: number; color?: string } = $props();
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
  const roleTones: Partial<Record<Tone, string>> = {
    running: "animate-blink bg-role",
    hollow: "shadow-[inset_0_0_0_1.5px_color-mix(in_oklab,var(--role)_var(--role-depth),#000)]",
    none: "",
  };
</script>

<span
  class="inline-block flex-none rounded-full {color ? (roleTones[tone] ?? 'bg-role') : tones[tone]}"
  style:width="{size}px"
  style:height="{size}px"
  style:--role={color}
  aria-hidden="true"
></span>
