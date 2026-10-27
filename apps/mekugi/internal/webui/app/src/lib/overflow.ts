// Observe the rendered single-line preview, including late font loading.
export function observeOverflow(node: HTMLElement, update: (clipped: boolean) => void): () => void {
  let frame = 0;
  const measure = () => {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => update(node.scrollWidth > node.clientWidth));
  };
  const observer = new ResizeObserver(measure);
  observer.observe(node);
  document.fonts.addEventListener("loadingdone", measure);
  return () => {
    cancelAnimationFrame(frame);
    observer.disconnect();
    document.fonts.removeEventListener("loadingdone", measure);
  };
}
