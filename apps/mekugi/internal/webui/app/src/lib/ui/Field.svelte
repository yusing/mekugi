<script lang="ts">
  import type { Snippet } from "svelte";

  // A labelled control, the label above it.
  let { label, children }: { label: string; children: Snippet } = $props();
</script>

<label class="grid min-w-0 gap-1.5"
  ><span class="font-medium">{label}</span>{@render children()}</label
>
