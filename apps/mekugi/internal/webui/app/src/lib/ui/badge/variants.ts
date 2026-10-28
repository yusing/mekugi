import { type VariantProps, tv } from "tailwind-variants";

export const badgeVariants = tv(
  {
    base: "flex-none font-medium whitespace-nowrap",
    variants: {
      // chrome is the stock compact badge for page chrome; tag sits in text.
      size: {
        tag: "inline-block rounded-chip px-[7px] text-tiny",
        chrome: "inline-flex h-5 items-center gap-1 overflow-hidden rounded-full px-2 text-xs",
      },
      variant: {
        default: "bg-neutral-tint text-neutral-ink",
        secondary: "bg-raised text-ink",
        destructive: "bg-negative-tint text-negative-ink",
        live: "bg-accent-tint text-accent-ink",
        role: "bg-neutral-tint text-role",
        outline: "border border-border text-ink",
        quiet: "border border-border text-muted",
        ghost: "text-muted",
        link: "text-ink underline-offset-4 hover:underline",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "tag",
    },
  },
  { twMerge: false },
);

export type BadgeVariant = VariantProps<typeof badgeVariants>["variant"];
export type BadgeSize = VariantProps<typeof badgeVariants>["size"];
