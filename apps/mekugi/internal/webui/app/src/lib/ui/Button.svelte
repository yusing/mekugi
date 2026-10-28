<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLButtonAttributes } from "svelte/elements";
  import { Button as ButtonPrimitive } from "./button";
  import Icon, { type IconName } from "./Icon.svelte";
  import { buttonVariant, type Shape, type Size, type Variant } from "./styles";

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

<ButtonPrimitive
  bind:ref={() => ref ?? null, (element) => (ref = element ?? undefined)}
  {type}
  variant={buttonVariant[variant]}
  shape={shape ?? (children ? "text" : "icon")}
  {size}
  class={extra}
  aria-label={label}
  title={label}
  {...rest}
>
  {#if icon}<Icon name={icon} />{/if}{@render children?.()}
</ButtonPrimitive>
