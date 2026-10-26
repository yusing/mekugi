// Class recipes shared by the UI kit and by elements that only look like a
// control. Each recipe owns the properties it sets; callers add layout
// (margins, flex placement) but never restyle what a recipe sets, since two
// utilities for one property have no reliable order.

export type Variant = "default" | "primary" | "danger" | "quiet" | "quiet-danger";
export type Shape = "text" | "icon" | "round";
export type Size = "sm" | "md" | "lg";

// One primary (near-black) action per surface; everything else is outlined
// or quiet. Phones get 44px targets.
const variants: Record<Variant, string> = {
  default: "border-border bg-surface text-ink hover:enabled:border-control",
  primary: "border-primary bg-primary text-on-primary hover:enabled:opacity-88",
  danger: "border-border bg-surface text-negative hover:enabled:border-negative",
  quiet:
    "border-transparent bg-transparent text-muted hover:enabled:bg-active hover:enabled:text-ink aria-pressed:bg-active aria-pressed:text-ink aria-expanded:bg-active aria-expanded:text-ink",
  "quiet-danger": "border-transparent bg-transparent text-negative hover:enabled:bg-active",
};
const heights: Record<Size, string> = {
  sm: "min-h-7 max-sm:min-h-11",
  md: "min-h-8 max-sm:min-h-11",
  lg: "min-h-11",
};
const shapes: Record<Size, Record<Shape, string>> = {
  sm: { text: "px-2.5", icon: "w-7 max-sm:w-11", round: "w-7 rounded-full max-sm:w-11" },
  md: { text: "px-3", icon: "w-8 max-sm:w-11", round: "w-8 rounded-full max-sm:w-11" },
  lg: { text: "px-3.5", icon: "w-11", round: "w-11 rounded-full" },
};

export function button(variant: Variant = "default", shape: Shape = "text", size: Size = "md") {
  return [
    "inline-flex flex-none items-center justify-center gap-1.5 rounded-control border font-medium whitespace-nowrap disabled:opacity-45",
    variants[variant],
    heights[size],
    shapes[size][shape],
  ].join(" ");
}

export const input =
  "min-h-9 w-full min-w-0 rounded-control border border-control bg-surface px-2.5 py-[7px] text-ink aria-invalid:border-negative max-sm:min-h-11 max-sm:text-base";

// What floats over the page: menus, popovers, suggestions and toasts.
export const floating = "rounded-float border border-border bg-surface text-ink shadow-float";

// A toolbar picker's pill. It shows what is chosen; its popover changes it.
export const chip =
  "inline-flex h-[30px] min-w-0 max-w-full items-center gap-1.5 rounded-full border px-2.5 text-small text-muted hover:enabled:border-control hover:enabled:text-ink aria-expanded:border-control aria-expanded:text-ink disabled:opacity-45 max-sm:h-10 max-sm:text-body";

export const menuRule = "mx-1.5 my-1 h-px bg-rule";

// A segmented track whose selected segment is a raised pill; callers choose
// whether the track fills its row.
export const segments = "gap-1 rounded-card bg-raised p-0.5";
export const segment =
  "min-h-[30px] min-w-0 truncate rounded-control px-3 font-medium text-muted disabled:cursor-default aria-pressed:bg-surface aria-pressed:text-ink aria-pressed:shadow-[0_0_0_1px_var(--border)] aria-selected:bg-surface aria-selected:text-ink aria-selected:shadow-[0_0_0_1px_var(--border)]";

// An empty section keeps its frame and says so in one sentence.
export const emptyBox = "rounded-card border border-dashed border-control px-3.5 py-2.5 text-muted";

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
