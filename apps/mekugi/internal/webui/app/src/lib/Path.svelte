<script lang="ts">
  // A workspace path that may wrap after a slash, never mid-name.
  let { path }: { path: string } = $props();

  const parts = $derived(path.split(/(?<=\/)/));
</script>

{#each parts as part, i (i)}{part}<wbr />{/each}
