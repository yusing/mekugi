<script lang="ts">
  import { tick } from "svelte";
  import type { Caption, ChoiceID } from "../../../../sessionapi/protocol";
  import { semantic } from "./semantic.svelte";
  import Icon from "./ui/Icon.svelte";
  import { chip, floating } from "./ui/styles";

  // Main's model and reasoning effort. Each chip runs the terminal's /model or
  // /effort command, whose menu lists the thread's own choices above the chips.
  let { caption, disabled = false }: { caption: Caption; disabled?: boolean } = $props();
  let root = $state<HTMLElement>();
  let list = $state<HTMLElement>();
  let opening: Promise<void> | undefined;
  let changing = false;
  let tierPending = $state(false);
  const fast = $derived(caption.Tier === "fast" || caption.Tier === "priority");
  async function toggleFast() {
    if (tierPending) return;
    tierPending = true;
    try {
      if (menu) await choose(1);
      await semantic.send({ command: `/tier ${fast ? "default" : "priority"}` });
    } finally {
      tierPending = false;
    }
  }
  let dismissed = $state(false);
  const menu = $derived(dismissed ? undefined : semantic.settingsMenu);
  const effort = $derived(!!menu && menu.Setting !== "/model");
  const settings = [
    { command: "/model", label: "Model", value: () => caption.Model || "Default model" },
    {
      command: "/effort",
      label: "Reasoning effort",
      value: () => caption.Effort || "Default effort",
    },
  ];

  // Choosing or closing hides the menu at once; a menu the server opens in
  // response, such as effort after a model, shows once the choice settles.
  async function choose(kind: number, choice: ChoiceID = { ID: 0, Name: "", Path: "" }) {
    const attachment = semantic.event?.attachment;
    const setting = menu?.Setting;
    dismissed = true;
    if (kind === 1) await opening;
    const view = semantic.frame?.view;
    const live = view?.Menu;
    if (
      semantic.event?.attachment === attachment &&
      view &&
      live?.Kind === "settings" &&
      live.Setting === setting
    )
      await semantic.send({
        menu: { Version: view.Version, Menu: live.ID, Kind: kind, Choice: choice, Query: "" },
        editor: semantic.metadata(),
      });
    dismissed = false;
  }
  // Closing during acquisition hides the preview immediately and dismisses
  // only the fresh server menu once its identity arrives.
  async function toggle(command: string) {
    if (changing) return;
    changing = true;
    const other = !menu || effort === (command === "/model");
    if (menu) await choose(1);
    if (other) {
      opening = semantic.openSetting(command);
      await opening;
      opening = undefined;
    }
    changing = false;
  }
  function options(): HTMLElement[] {
    return Array.from(list?.querySelectorAll<HTMLElement>("[role=option]") ?? []);
  }
  // The current choice takes focus once the menu's choices arrive.
  $effect(() => {
    if (!menu?.Choices.length || menu.Loading || semantic.pending) return;
    void tick().then(() =>
      (
        options().find((option) => option.getAttribute("aria-selected") === "true") ?? options()[0]
      )?.focus(),
    );
  });
  function onkeydown(event: KeyboardEvent) {
    if (!menu) return;
    if (event.key === "Escape") {
      event.preventDefault();
      choose(1);
      return;
    }
    const all = options();
    const at = all.indexOf(document.activeElement as HTMLElement);
    if (at < 0 || (event.key !== "ArrowDown" && event.key !== "ArrowUp")) return;
    event.preventDefault();
    all[(at + (event.key === "ArrowDown" ? 1 : all.length - 1)) % all.length].focus();
  }
</script>

<svelte:window
  {onkeydown}
  onpointerdown={(event) => {
    if (menu && !root?.contains(event.target as Node)) choose(1);
  }}
/>

<span class="relative ml-1 flex min-w-0 gap-1" bind:this={root}>
  {#each settings as setting (setting.command)}
    <button
      type="button"
      class="{chip} max-w-48"
      title={setting.label}
      {disabled}
      aria-haspopup="listbox"
      aria-expanded={!!menu && effort === (setting.command !== "/model")}
      onclick={() => toggle(setting.command)}
    >
      <span class="sr-only">{setting.label}:</span>
      <span class="min-w-0 truncate text-left text-ink">{setting.value()}</span>
      <span class="flex flex-none max-sm:hidden"><Icon name="chevron" size={14} /></span>
    </button>
  {/each}
  <button
    type="button"
    class={[chip, "flex-none", fast && "border-warning-edge bg-warning-tint"]}
    title={fast ? "Disable fast mode" : "Enable fast mode"}
    aria-pressed={fast}
    aria-busy={tierPending}
    disabled={disabled || tierPending || semantic.pending > 0}
    onclick={toggleFast}
    ><span class={["flex items-center gap-1.5", fast && "text-warning-ink"]}
      ><Icon name="flash" size={14} /><span>Fast</span></span
    ></button
  >
  {#if menu}
    <div
      class="menu-enter absolute bottom-[calc(100%+6px)] left-0 z-5 flex max-h-[min(360px,45dvh)] w-max max-w-[min(360px,calc(100vw-32px))] min-w-56 flex-col p-1 {floating}"
    >
      {#if menu.Loading && !menu.Choices.length}<p
          class="px-2.5 py-1.5 text-small text-muted"
          role="status"
        >
          Loading choices…
        </p>
      {:else if menu.Problem}<p class="px-2.5 py-1.5 text-small text-muted">
          {menu.Problem}
        </p>{/if}
      <div
        bind:this={list}
        class="min-h-0 overflow-y-auto"
        role="listbox"
        aria-label={effort ? "Reasoning effort" : "Model"}
      >
        {#each menu.Choices as choice (JSON.stringify([choice.ID, choice.Name, choice.Path]))}
          {@const current = choice.ID === menu.Preferred.ID && choice.Name === menu.Preferred.Name}
          <button
            type="button"
            role="option"
            tabindex="-1"
            aria-selected={current}
            class="grid min-h-[34px] w-full grid-cols-[16px_minmax(0,1fr)] items-center gap-x-2 rounded-control py-1.5 pr-3 pl-2 text-left text-ink hover:bg-active focus-visible:bg-active focus-visible:outline-none disabled:opacity-45 max-sm:min-h-11"
            disabled={menu.Loading || menu.Saving || semantic.pending > 0}
            onclick={() => choose(2, { ID: choice.ID, Name: choice.Name, Path: choice.Path })}
          >
            <span class="flex text-muted"
              >{#if current}<Icon name="check" />{/if}</span
            >
            <span class="truncate">{choice.Display || choice.Name}</span>
          </button>
        {/each}
      </div>
    </div>
  {/if}
</span>
