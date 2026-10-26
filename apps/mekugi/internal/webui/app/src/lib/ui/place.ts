// Places a fixed popover beside the button that opens it: above the button
// when there is more room there, its left edge kept 8px inside the viewport
// for the given width. Returns the popover's inline style; --room is the
// height left on the chosen side, for the caller's max-height.
export function beside(button: HTMLElement, width: number): string {
  const rect = button.getBoundingClientRect();
  const left = Math.max(8, Math.min(rect.left, innerWidth - 8 - width));
  return rect.top > innerHeight - rect.bottom
    ? `left:${left}px;bottom:${innerHeight - rect.top + 6}px;--room:${rect.top - 14}px`
    : `left:${left}px;top:${rect.bottom + 6}px;--room:${innerHeight - rect.bottom - 14}px`;
}
