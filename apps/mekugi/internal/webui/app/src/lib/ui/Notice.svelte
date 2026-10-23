<script lang="ts">
  import type { Snippet } from "svelte";

  // One quiet sentence about the state of what is shown: a thread whose
  // machine is missing, a lost connection, a creation note.
  let {
    role = "status",
    class: extra = "",
    children,
  }: { role?: "status" | "alert"; class?: string; children: Snippet } = $props();
</script>

<p
  class="rounded-card border border-warning-edge bg-warning-tint px-3 py-1.5 text-small text-warning-ink {extra}"
  {role}
>
  {@render children()}
</p>
