<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLButtonAttributes } from "svelte/elements";
  import Icon, { type IconName } from "./Icon.svelte";
  import { button, type Shape, type Size, type Variant } from "./styles";

  // A button from the shared recipe. An icon without text is labelled by
  // label, which also titles it; class adds layout only.
  let {
    variant = "default",
    shape,
    size = "md",
    icon,
    label,
    type = "button",
    ref = $bindable(),
    class: extra = "",
    children,
    ...rest
  }: Omit<HTMLButtonAttributes, "class"> & {
    variant?: Variant;
    shape?: Shape;
    size?: Size;
    icon?: IconName;
    label?: string;
    ref?: HTMLButtonElement;
    class?: string;
    children?: Snippet;
  } = $props();
</script>

<button
  bind:this={ref}
  {type}
  class="{button(variant, shape ?? (children ? 'text' : 'icon'), size)} {extra}"
  aria-label={label}
  title={label}
  {...rest}
>
  {#if icon}<Icon name={icon} />{/if}{@render children?.()}
</button>
