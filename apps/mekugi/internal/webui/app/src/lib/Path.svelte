<script lang="ts">
  // Geometry-only elision. Keep the full source and its original LTR order for
  // selection, accessibility and copy while the overflow marker sits on the left.
  let { path, title = path }: { path: string; title?: string } = $props();
</script>

<span class="inline-block max-w-full min-w-0 truncate text-left align-bottom" dir="rtl" {title}
  ><bdi dir="ltr">{path}</bdi></span
>
