<script lang="ts">
  // Mekugi's mark: a rounded square in the accent around a ring.
  let { size = 20 }: { size?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true">
  <rect x="1" y="1" width="18" height="18" rx="5" fill="var(--accent)" />
  <circle cx="10" cy="10" r="3.2" fill="none" stroke="var(--surface)" stroke-width="1.8" />
</svg>
