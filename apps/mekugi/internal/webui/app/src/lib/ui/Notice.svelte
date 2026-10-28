<script lang="ts">
  import { Alert } from "./alert";
  import type { Snippet } from "svelte";

  // One quiet sentence about the state of what is shown: a thread whose
  // machine is missing, a lost connection, a creation note.
  let {
    role = "status",
    class: extra = "",
    children,
  }: { role?: "status" | "alert"; class?: string; children: Snippet } = $props();
</script>

<Alert variant="warning" class={extra} {role}>
  {@render children()}
</Alert>
