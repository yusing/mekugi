import { type VariantProps, tv } from "tailwind-variants";
import type { WithElementRef } from "$lib/utils.js";
import type { HTMLButtonAttributes } from "svelte/elements";

export const buttonVariants = tv(
  {
    base: "inline-flex flex-none items-center justify-center gap-1.5 rounded-control border font-medium whitespace-nowrap disabled:opacity-45 [&_svg]:pointer-events-none [&_svg]:shrink-0",
    variants: {
      variant: {
        default: "border-primary bg-primary text-on-primary hover:enabled:opacity-88",
        outline: "border-border bg-surface text-ink hover:enabled:border-control",
        secondary: "border-transparent bg-raised text-ink hover:enabled:bg-active",
        ghost:
          "border-transparent bg-transparent text-muted hover:enabled:bg-active hover:enabled:text-ink aria-pressed:bg-active aria-pressed:text-ink aria-expanded:bg-active aria-expanded:text-ink",
        destructive: "border-border bg-surface text-negative hover:enabled:border-negative",
        "quiet-danger": "border-transparent bg-transparent text-negative hover:enabled:bg-active",
        link: "border-transparent text-ink underline-offset-4 hover:underline",
      },
      size: {
        default: "min-h-8 px-3 max-sm:min-h-11",
        xs: "min-h-6 px-2 text-small max-sm:min-h-11",
        sm: "min-h-7 px-2.5 max-sm:min-h-11",
        md: "min-h-8 px-3 max-sm:min-h-11",
        lg: "min-h-11 px-3.5",
        icon: "size-8 p-0 max-sm:size-11",
        "icon-xs": "size-6 p-0 max-sm:size-11",
        "icon-sm": "size-7 p-0 max-sm:size-11",
        "icon-lg": "size-11 p-0",
      },
      shape: {
        text: "",
        icon: "px-0",
        round: "rounded-full px-0",
      },
    },
    compoundVariants: [
      { size: "sm", shape: ["icon", "round"], class: "w-7 max-sm:w-11" },
      { size: ["md", "default"], shape: ["icon", "round"], class: "w-8 max-sm:w-11" },
      { size: "lg", shape: ["icon", "round"], class: "w-11" },
    ],
    defaultVariants: { variant: "default", size: "default", shape: "text" },
  },
  { twMerge: false },
);

export type ButtonVariant = VariantProps<typeof buttonVariants>["variant"];
export type ButtonSize = VariantProps<typeof buttonVariants>["size"];

export type ButtonProps = WithElementRef<HTMLButtonAttributes, HTMLButtonElement> & {
  variant?: ButtonVariant;
  size?: ButtonSize;
  shape?: "text" | "icon" | "round";
};
