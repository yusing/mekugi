// visiblyPainted attaches to a node and calls painted once a paint shows part
// of it unobscured: inside the viewport and every clipping ancestor, with the
// element in front at that part's centre belonging to it. Hidden documents,
// hidden or transparent nodes and covering dialogs report nothing. It checks
// again only when visibility, scrolling or covering DOM changes.
export const visiblyPainted = (painted: () => void) => (node: HTMLElement) => {
  let frame = 0;
  // Observing again reports the current intersection, after layout and paint.
  const intersection = new IntersectionObserver(
    (entries) => {
      const entry = entries.at(-1);
      if (
        !entry?.isIntersecting ||
        document.visibilityState !== "visible" ||
        !node.checkVisibility({ opacityProperty: true, visibilityProperty: true })
      )
        return;
      const { left, right, top, bottom } = entry.intersectionRect;
      if (right <= left || bottom <= top) return;
      const front = document.elementFromPoint((left + right) / 2, (top + bottom) / 2);
      if (front && node.contains(front)) painted();
    },
    { threshold: [0, 1] },
  );
  const schedule = () => {
    if (!frame)
      frame = requestAnimationFrame(() => {
        frame = 0;
        intersection.unobserve(node);
        intersection.observe(node);
      });
  };
  const covered = new MutationObserver(schedule);
  covered.observe(document.body, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["inert", "aria-hidden", "data-state"],
  });
  document.addEventListener("visibilitychange", schedule);
  window.addEventListener("scroll", schedule, true);
  intersection.observe(node);
  return () => {
    cancelAnimationFrame(frame);
    intersection.disconnect();
    covered.disconnect();
    document.removeEventListener("visibilitychange", schedule);
    window.removeEventListener("scroll", schedule, true);
  };
};
