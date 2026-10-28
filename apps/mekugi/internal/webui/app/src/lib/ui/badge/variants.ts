import { type VariantProps, tv } from "tailwind-variants";

export const badgeVariants = tv(
  {
    base: "inline-block flex-none rounded-chip px-[7px] text-tiny font-medium whitespace-nowrap",
    variants: {
      variant: {
        default: "bg-neutral-tint text-neutral-ink",
        secondary: "bg-raised text-ink",
        destructive: "bg-negative-tint text-negative-ink",
        live: "bg-accent-tint text-accent-ink",
        role: "bg-neutral-tint text-role",
        outline: "border border-border text-ink",
        ghost: "text-muted",
        link: "text-ink underline-offset-4 hover:underline",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
  { twMerge: false },
);

export type BadgeVariant = VariantProps<typeof badgeVariants>["variant"];
