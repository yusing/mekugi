<script lang="ts">
  import type { NativeEvent, RemoteOperation } from "../../../../sessionapi/protocol";
  import { semantic } from "./semantic.svelte";
  import Button from "./ui/Button.svelte";

  // Offers a selection within root as a reference once it is made: above the
  // pointer that ended it, or above the end the keyboard moves.
  let {
    root,
    source = "",
    description = "message",
    onreference,
  }: {
    root?: HTMLElement;
    source?: string;
    description?: string;
    onreference?: () => void;
  } = $props();
  // The event and operation stay as captured. at is the anchor point, kept
  // beside the selection's text as it scrolls by following its range.
  let captured = $state<{
    event: NativeEvent;
    operation: RemoteOperation;
    range: Range;
    start: Node;
    end: Node;
    origin: DOMRect;
    at: { x: number; y: number };
  }>();
  const available = $derived(
    captured?.event.attachment === semantic.event?.attachment &&
      captured?.operation.reference?.Thread === semantic.frame?.view.Input.ID,
  );
  let viewportWidth = $state(0);
  let viewportHeight = $state(0);
  let button = $state<HTMLButtonElement>();
  $effect(() => {
    if (!captured || !root) return;
    const { start, end } = captured;
    const content = root;
    // Live ranges migrate to an ancestor when their selected row disappears.
    // Watch only while a selection is captured, retaining the original nodes.
    const observer = new MutationObserver(() => {
      if (!content.contains(start) || !content.contains(end)) captured = undefined;
    });
    observer.observe(content, { childList: true, subtree: true });
    return () => observer.disconnect();
  });
  // Centred above the anchor, below it where the top has no room, and 8px
  // inside the viewport either way.
  const place = $derived.by(() => {
    if (!captured || !button) return "visibility:hidden";
    const { width, height } = button.getBoundingClientRect();
    const { x, y } = captured.at;
    const left = Math.max(8, Math.min(x - width / 2, viewportWidth - 8 - width));
    const above = y - height - 8;
    const top = Math.max(8, Math.min(above < 8 ? y + 20 : above, viewportHeight - 8 - height));
    return `left:${left}px;top:${top}px`;
  });

  // A primary pointer pressed elsewhere is still making the selection; its
  // release completes it. A completed selection keeps the anchor its
  // completion gave it.
  let held = false;
  const own = (event: Event) => !!button?.contains(event.target as Node);
  function unchanged() {
    const selection = document.getSelection();
    if (
      !captured ||
      !root ||
      !selection?.rangeCount ||
      selection.isCollapsed ||
      !root.contains(selection.anchorNode) ||
      !root.contains(selection.focusNode)
    )
      return false;
    const range = selection.getRangeAt(0);
    return (
      range.compareBoundaryPoints(Range.START_TO_START, captured.range) === 0 &&
      range.compareBoundaryPoints(Range.END_TO_END, captured.range) === 0
    );
  }
  function down(event: PointerEvent) {
    if (event.button === 0 && !own(event)) held = true;
  }
  function up(event: PointerEvent) {
    if (own(event)) return;
    held = false;
    if (!unchanged()) select({ x: event.clientX, y: event.clientY });
  }
  function cancel() {
    held = false;
    if (!unchanged()) select();
  }
  function changed() {
    if (held) captured = undefined;
    else if (!unchanged()) select();
  }
  function select(pointer?: { x: number; y: number }) {
    const selection = document.getSelection();
    const event = semantic.event;
    if (
      !root ||
      !selection ||
      selection.isCollapsed ||
      !event ||
      !root.contains(selection.anchorNode) ||
      !root.contains(selection.focusNode)
    ) {
      captured = undefined;
      return;
    }
    const text = selection.toString();
    if (!text.trim()) {
      captured = undefined;
      return;
    }
    const range = selection.getRangeAt(0).cloneRange();
    const view = event.frame.view;
    captured = {
      event,
      operation: {
        reference: {
          Version: view.Version,
          Thread: view.Input.ID,
          Displayed: view.Displayed,
          Target: semantic.target(),
          Description: description,
          Text: text,
          Source: source,
        },
        editor: semantic.metadata(),
      },
      range,
      start: range.startContainer,
      end: range.endContainer,
      origin: range.getBoundingClientRect(),
      at: pointer ?? endpoint(selection, range),
    };
  }
  // The selection's moving end, where the keyboard left it.
  function endpoint(selection: Selection, range: Range) {
    const caret = document.createRange();
    caret.setStart(selection.focusNode!, selection.focusOffset);
    const rect = caret.getClientRects()[0] ?? range.getBoundingClientRect();
    return { x: rect.left, y: rect.top };
  }
  function scrolled() {
    if (!captured) return;
    const { range, origin, at } = captured;
    const moved = range.getBoundingClientRect();
    captured.at = { x: at.x + moved.left - origin.left, y: at.y + moved.top - origin.top };
    captured.origin = moved;
  }
  async function reference() {
    const selection = captured;
    if (!selection || !available) return;
    const error = await semantic.send(selection.operation, undefined, false, selection.event);
    if (!error) {
      captured = undefined;
      document.getSelection()?.removeAllRanges();
      onreference?.();
    }
  }
</script>

<svelte:window bind:innerWidth={viewportWidth} bind:innerHeight={viewportHeight} />

<svelte:document
  onselectionchange={changed}
  onpointerdown={down}
  onpointerup={up}
  onpointercancel={cancel}
  onscrollcapture={scrolled}
/>

{#if captured && available}
  <Button
    bind:ref={button}
    icon="attach"
    label="Copy selection to reference"
    class="fixed z-20 shadow-float"
    style={place}
    disabled={semantic.connection !== "open" || semantic.pending > 0}
    onpointerdown={(event) => event.preventDefault()}
    onclick={reference}>Reference</Button
  >
{/if}
