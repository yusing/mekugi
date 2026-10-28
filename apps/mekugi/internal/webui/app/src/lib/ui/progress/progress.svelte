<script lang="ts">
  import { Progress as ProgressPrimitive } from "bits-ui";
  import { cn, type WithoutChildrenOrChild } from "$lib/utils.js";

  let {
    ref = $bindable(null),
    class: className,
    max = 100,
    value,
    ...restProps
  }: WithoutChildrenOrChild<ProgressPrimitive.RootProps> = $props();
</script>

<ProgressPrimitive.Root
  bind:ref
  data-slot="progress"
  class={cn(
    "bg-raised relative flex h-1 w-full items-center overflow-hidden rounded-full",
    className,
  )}
  {value}
  {max}
  {...restProps}
>
  {#snippet child({ props })}
    <!-- Context indicators also appear inside agent navigation buttons. -->
    <span {...props}>
      <span
        data-slot="progress-indicator"
        class="bg-primary size-full flex-1 transition-transform motion-reduce:transition-none"
        style="transform: translateX(-{100 - (100 * (value ?? 0)) / (max ?? 1)}%)"
      ></span>
    </span>
  {/snippet}
</ProgressPrimitive.Root>
