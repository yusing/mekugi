// Fixed and editable pickers share the Select's Nova presentation.
export const selectTrigger =
  "border-input data-placeholder:text-muted-foreground dark:bg-input/30 dark:hover:bg-input/50 focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive dark:aria-invalid:border-destructive/50 gap-1.5 rounded-lg border bg-transparent py-2 pr-2 pl-2.5 text-sm transition-colors select-none focus-visible:ring-3 aria-invalid:ring-3 data-[size=default]:h-8 data-[size=sm]:h-7 data-[size=sm]:rounded-[min(var(--radius-md),10px)] *:data-[slot=select-value]:gap-1.5 [&_svg:not([class*='size-'])]:size-4 flex w-fit min-w-0 max-w-full items-center justify-between whitespace-nowrap outline-none disabled:cursor-not-allowed disabled:opacity-50 *:data-[slot=select-value]:line-clamp-1 *:data-[slot=select-value]:flex *:data-[slot=select-value]:items-center [&_svg]:pointer-events-none [&_svg]:shrink-0 aria-expanded:bg-ui-muted aria-expanded:text-foreground max-sm:min-h-11 max-sm:min-w-11";

export const selectContent =
  "bg-popover text-popover-foreground data-open:animate-in data-closed:animate-out data-closed:fade-out-0 data-open:fade-in-0 data-closed:zoom-out-95 data-open:zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 ring-foreground/10 rounded-lg shadow-md ring-1 duration-100 relative z-50 w-max max-w-[min(360px,calc(100vw-16px))] overflow-x-hidden overflow-y-auto";

// The composer's Fast toggle sits among the setting triggers; pressed shows the
// warning tint that marks a priority tier.
export const settingToggle =
  "border-input dark:bg-input/30 hover:bg-ui-muted dark:hover:bg-input/50 hover:text-foreground focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3 inline-flex h-8 flex-none items-center gap-1.5 rounded-lg border bg-transparent px-2.5 text-sm text-muted-foreground transition-colors outline-none select-none disabled:cursor-not-allowed disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 aria-pressed:border-warning-edge aria-pressed:bg-warning-tint aria-pressed:text-warning-ink aria-pressed:hover:bg-warning-tint aria-pressed:hover:text-warning-ink dark:aria-pressed:bg-warning-tint dark:aria-pressed:hover:bg-warning-tint max-sm:min-h-11 max-sm:min-w-11";

// A row in a hand-built listbox, matching the Select's items.
export const optionRow =
  "relative flex w-full cursor-default items-center gap-1.5 rounded-md py-1 pr-8 pl-1.5 text-left text-sm text-foreground outline-hidden select-none hover:bg-ui-accent focus-visible:bg-ui-accent disabled:pointer-events-none disabled:opacity-50 max-sm:min-h-11";
