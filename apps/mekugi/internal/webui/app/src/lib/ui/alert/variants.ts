import { type VariantProps, tv } from "tailwind-variants";

export const alertVariants = tv(
  {
    base: "text-small",
    variants: {
      variant: {
        default: "rounded-card border border-border bg-surface px-3 py-1.5 text-ink",
        destructive: "text-negative",
        warning:
          "rounded-card border border-warning-edge bg-warning-tint px-3 py-1.5 text-warning-ink",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
  { twMerge: false },
);

export type AlertVariant = VariantProps<typeof alertVariants>["variant"];
