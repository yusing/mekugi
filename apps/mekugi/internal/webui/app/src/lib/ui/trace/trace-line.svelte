<script lang="ts">
  import { cn, type WithElementRef } from "$lib/utils.js";
  import type { HTMLAttributes } from "svelte/elements";
  import type { TraceLineStatus } from "./index.js";

  let {
    ref = $bindable(null),
    class: className,
    status,
    children,
    ...restProps
  }: WithElementRef<HTMLAttributes<HTMLSpanElement>> & {
    status?: TraceLineStatus;
  } = $props();
</script>

<span
  bind:this={ref}
  data-slot="trace-line"
  data-status={status}
  class={cn(
    "gap-2 text-sm flex w-fit max-w-full min-w-0 items-center text-muted-foreground transition-colors in-[a,button]:cursor-pointer in-[a:hover,button:hover]:text-foreground data-[status=error]:text-destructive data-[status=error]:in-[a:hover,button:hover]:text-destructive",
    className,
  )}
  {...restProps}
>
  {@render children?.()}
</span>
