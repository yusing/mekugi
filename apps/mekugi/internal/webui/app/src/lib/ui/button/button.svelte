<script lang="ts">
  import { cn } from "$lib/utils.js";
  import { buttonVariants, type ButtonProps } from "./variants";
  let {
    class: className,
    variant = "default",
    size = "default",
    shape = "text",
    ref = $bindable(null),
    type = "button",
    disabled,
    children,
    ...restProps
  }: ButtonProps = $props();
</script>

<button
  bind:this={ref}
  data-slot="button"
  class={cn(buttonVariants({ variant, size, shape }), className)}
  {type}
  {disabled}
  {...restProps}
>
  {@render children?.()}
</button>
