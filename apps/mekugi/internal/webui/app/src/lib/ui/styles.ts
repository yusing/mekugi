import { buttonVariants } from "./button/variants";
import { cn } from "../utils";

// Class recipes shared by the UI kit and by elements that only look like a
// control. Each recipe owns the properties it sets; callers add layout
// (margins, flex placement) but never restyle what a recipe sets, since two
// utilities for one property have no reliable order.

export type Variant = "default" | "primary" | "danger" | "quiet" | "quiet-danger";
export type Shape = "text" | "icon" | "round";
export type Size = "sm" | "md" | "lg";

// The local shadcn button owns control styling; this recipe also styles
// native controls that cannot render a component.
export const buttonVariant = {
  default: "outline",
  primary: "default",
  danger: "destructive",
  quiet: "ghost",
  "quiet-danger": "quiet-danger",
} as const;

export function button(variant: Variant = "default", shape: Shape = "text", size: Size = "md") {
  return cn(buttonVariants({ variant: buttonVariant[variant], shape, size }));
}

export const input =
  "min-h-9 w-full min-w-0 rounded-control border border-control bg-surface px-2.5 py-[7px] text-ink aria-invalid:border-negative max-sm:min-h-11 max-sm:text-base";

// What floats over the page: menus, popovers, suggestions and toasts.
export const floating = "rounded-float border border-border bg-surface text-ink shadow-float";

// A toolbar picker's pill. It shows what is chosen; its popover changes it.
export const chip =
  "inline-flex h-[30px] min-w-0 max-w-full items-center gap-1.5 rounded-control bg-raised px-2.5 text-small text-muted hover:enabled:bg-active hover:enabled:text-ink aria-expanded:bg-active aria-expanded:text-ink disabled:opacity-45 max-sm:h-10 max-sm:text-body";

export const menuRule = "mx-1.5 my-1 h-px bg-rule";

// A segmented track whose selected segment is a raised pill; callers choose
// whether the track fills its row.
export const segments = "gap-1 rounded-card bg-raised p-0.5";
export const segment =
  "min-h-[30px] min-w-0 truncate rounded-control px-3 font-medium text-muted disabled:cursor-default aria-pressed:bg-surface aria-pressed:text-ink aria-selected:bg-surface aria-selected:text-ink";

// A transcript row's action in the gutter to its right; callers place it and
// choose when it shows, usually with revealed.
export const rowAction =
  "flex size-7 items-center justify-center rounded-control hover:bg-active hover:text-ink max-sm:size-9";
export const revealed =
  "opacity-0 group-hover/row:opacity-100 focus-visible:opacity-100 touch:opacity-100";

// Empty sections use quiet spacing rather than another frame.
export const emptyBox = "px-3.5 py-2.5 text-muted";

// Code, output and file text on a raised well; callers pad it.
export const codeBlock = "overflow-auto rounded-card bg-raised text-small leading-[18px]";

// A flat placeholder at the size of what is loading.
export const skeleton = "rounded-chip bg-active";

// A disclosure's triangle, pointing right while closed. Summaries rotate it
// inside an open group/disclosure; buttons as their aria-expanded says.
const triangle =
  "before:border-y-[3.5px] before:border-l-4 before:border-y-transparent before:border-l-current before:transition-transform";
export const caret = `${triangle} group-open/disclosure:before:rotate-90`;
export const caretButton = `${triangle} before:rotate-90 aria-expanded:before:-rotate-90`;

// Composers share one frame; their text grows with its content.
export const composerText =
  "block max-h-[40dvh] w-full resize-none border-0 bg-transparent px-3.5 leading-[22px] text-ink field-sizing-content focus-visible:outline-none max-sm:text-base";
export const composerTools = "flex items-center px-2 pt-1 pb-2";
