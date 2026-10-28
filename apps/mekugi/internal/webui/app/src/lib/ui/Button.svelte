<script lang="ts">
  import type { Snippet } from "svelte";
  import type { HTMLButtonAttributes } from "svelte/elements";
  import { Button as ButtonPrimitive } from "./button";
  import * as Tooltip from "./tooltip";
  import Icon, { type IconName } from "./Icon.svelte";
  import { buttonVariant, type Shape, type Size, type Variant } from "./styles";

  // A button from the shared recipe. An icon without text is labelled by
  // label, which also titles it; class adds layout only. tooltip replaces the
  // native title with the kit's tooltip, shown while the trigger is hovered or
  // has keyboard focus.
  let {
    variant = "default",
    shape,
    size = "md",
    icon,
    label,
    title,
    tooltip = false,
    id,
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
    tooltip?: boolean;
    ref?: HTMLButtonElement;
    class?: string;
    children?: Snippet;
  } = $props();
</script>

{#snippet control(props: Record<string, unknown>, native?: string | null)}
  <ButtonPrimitive
    bind:ref={() => ref ?? null, (element) => (ref = element ?? undefined)}
    {type}
    variant={buttonVariant[variant]}
    shape={shape ?? (children ? "text" : "icon")}
    {size}
    class={extra}
    aria-label={label}
    title={native}
    {id}
    {...props}
  >
    {#if icon}<Icon name={icon} />{/if}{@render children?.()}
  </ButtonPrimitive>
{/snippet}

{#if tooltip && (title ?? label)}
  <Tooltip.Root>
    <Tooltip.Trigger id={id ?? undefined} {type} {...rest}>
      {#snippet child({ props })}{@render control(props)}{/snippet}
    </Tooltip.Trigger>
    <Tooltip.Content sideOffset={6} side="bottom">{title ?? label}</Tooltip.Content>
  </Tooltip.Root>
{:else}
  {@render control(rest, title ?? label)}
{/if}
