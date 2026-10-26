<script lang="ts" module>
  // 16px outline icons at a 1.5px stroke, drawn on a 24px grid.
  const paths = {
    play: "M7 5v14l11-7z",
    pause: "M8 5v14M16 5v14",
    back: "M11 17l-5-5 5-5M18 17l-5-5 5-5",
    forward: "M13 17l5-5-5-5M6 17l5-5-5-5",
    restart: "M4 12a8 8 0 1 0 2.3-5.6M4 4v4h4",
    minus: "M6 12h12",
    plus: "M12 6v12M6 12h12",
    info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8h.01",
    hook: "M13 3L5 14h6l-1 7 8-11h-6z",
    flash: "M13 3L5 14h6l-1 7 8-11h-6z",
    thought: "M9 18h6M10 21h4M12 3a6 6 0 0 0-3.5 10.9V15h7v-1.1A6 6 0 0 0 12 3z",
    agents:
      "M9 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 19c0-3 2.7-5 6-5s6 2 6 5M16 5a3 3 0 0 1 0 6M18 14c2 .6 3 2.4 3 5",
    down: "M12 5v14M6 13l6 6 6-6",
    left: "M15 6l-6 6 6 6",
    send: "M12 19V5M6 11l6-6 6 6",
    stop: "M7 7h10v10H7z",
    sidebar: "M4 5h16v14H4zM9 5v14",
    close: "M6 6l12 12M18 6L6 18",
    server: "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01",
    attach: "M20 11l-8.5 8.5a5 5 0 0 1-7-7L13 4a3.5 3.5 0 0 1 5 5l-8.5 8.5a2 2 0 0 1-3-3L14 7",
    terminal: "M4 5h16v14H4zM8 10l3 2-3 2M13 15h3",
    slash: "M15 4L9 20",
    chevron: "M7 10l5 5 5-5",
    copy: "M9 9h11v11H9zM5 15H4V4h11v1",
    expand: "M14 4h6v6M10 20H4v-6M20 4l-7 7M4 20l7-7",
    folder: "M3 6h6l2 2h10v11H3z",
    presets: "M7 4h10v16l-5-4-5 4z",
    history: "M3 12a9 9 0 1 0 3-6.7M3 4v5h5M12 7v5l3 2",
    compose: "M4 20h4L19 9l-4-4L4 16zM14 6l4 4",
    check: "M5 12l5 5 9-10",
    diff: "M12 4v8M8 8h8M8 18h8",
  } as const;
  export type IconName = keyof typeof paths;
</script>

<script lang="ts">
  let { name, size = 16 }: { name: IconName; size?: number } = $props();
</script>

<svg
  width={size}
  height={size}
  viewBox="0 0 24 24"
  fill="none"
  stroke="currentColor"
  stroke-width="1.5"
  stroke-linecap="round"
  stroke-linejoin="round"
  aria-hidden="true"
>
  <path d={paths[name]} />
</svg>
