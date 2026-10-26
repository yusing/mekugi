<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLAttributes } from "svelte/elements";

  // One line saying why something failed, announced when it appears; class
  // adds layout only.
  let {
    class: extra = "",
    children,
    ...rest
  }: Omit<HTMLAttributes<HTMLParagraphElement>, "class" | "role"> & {
    class?: string;
    children: Snippet;
  } = $props();
</script>

<p class="text-small text-negative {extra}" role="alert" {...rest}>{@render children()}</p>
