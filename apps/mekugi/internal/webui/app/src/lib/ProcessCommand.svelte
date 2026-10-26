<script lang="ts">
  import Code from "./Code.svelte";
  import { newIdentity } from "./transport";
  import { beside } from "./ui/place";
  import { floating } from "./ui/styles";

  // A running command's first line, highlighted as shell. Hovering, focusing
  // or pressing it shows the whole command above the composer, where it can
  // be read and selected. Escape, or leaving both the command and the
  // command shown, hides it; a pressed or focused command keeps it until
  // focus moves on.
  let { command }: { command: string } = $props();

  const id = `command-${newIdentity()}`;
  const width = 560;
  const line = $derived(command.split("\n")[0]);
  let anchor = $state<HTMLElement>();
  let tip = $state<HTMLElement>();
  let place = $state("");
  let timer: ReturnType<typeof setTimeout> | undefined;

  // The popover's own state: its toggle event arrives after the change.
  const showing = () => !!tip?.matches(":popover-open");
  function show() {
    clearTimeout(timer);
    if (!anchor || !tip || showing()) return;
    place = beside(anchor, Math.min(width, innerWidth - 16));
    tip.showPopover();
  }
  // The pointer may cross the gap onto the command shown without hiding it.
  function hide(delay = 120) {
    clearTimeout(timer);
    timer = setTimeout(() => showing() && tip?.hidePopover(), delay);
  }
  // Leaving with the pointer hides only what hovering showed.
  const leave = () => anchor?.matches(":focus") || hide();

  // While shown, the command follows its button through scrolling, resizing
  // and layout changes, and Escape hides it wherever focus is, before the
  // page's own Escape handlers act. Reflow elsewhere, such as the composer
  // growing, moves the button without any event, so it is placed each frame.
  let open = $state(false);
  function onkeydown(event: KeyboardEvent) {
    if (event.key !== "Escape" || event.defaultPrevented || !showing()) return;
    event.preventDefault();
    hide(0);
  }
  $effect(() => {
    if (!open || !anchor) return;
    const button = anchor;
    let frame = 0;
    const follow = () => {
      place = beside(button, Math.min(width, innerWidth - 16));
      frame = requestAnimationFrame(follow);
    };
    follow();
    addEventListener("keydown", onkeydown, true);
    return () => {
      cancelAnimationFrame(frame);
      removeEventListener("keydown", onkeydown, true);
    };
  });
</script>

<button
  bind:this={anchor}
  type="button"
  class="block w-full min-w-0 rounded-chip text-left text-small"
  aria-describedby={id}
  onmouseenter={show}
  onmouseleave={leave}
  onfocus={show}
  onblur={() => tip?.matches(":hover") || hide()}
  onclick={show}
  ><code class="block truncate"
    ><Code code={line} lang="bash" />{#if line !== command}<span class="text-muted">{" …"}</span
      >{/if}</code
  ></button
>
<div
  bind:this={tip}
  {id}
  popover="manual"
  role="tooltip"
  class="fixed inset-auto m-0 max-h-[min(var(--room),320px)] w-max max-w-[min(560px,calc(100vw-16px))] overflow-auto px-3 py-2 {floating}"
  style={place}
  onmouseenter={show}
  onmouseleave={leave}
  ontoggle={(event) => (open = (event as ToggleEvent).newState === "open")}
>
  <pre class="text-small leading-[18px] whitespace-pre-wrap wrap-anywhere"><Code
      code={command}
      lang="bash"
    /></pre>
</div>
