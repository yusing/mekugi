<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import type { ChoiceID, StatusField } from "../../../../sessionapi/protocol";
  import { date } from "./format";
  import { semantic } from "./semantic.svelte";
  import Button from "./ui/Button.svelte";
  import Dialog from "./ui/Dialog.svelte";
  import Dot from "./ui/Dot.svelte";
  import Icon from "./ui/Icon.svelte";
  import { input, segment, segments } from "./ui/styles";
  import Tag from "./ui/Tag.svelte";

  // The server's dialogs: menus other than inline completions, the saved
  // thread picker, the status panel and the copy fallback.
  let query = $state("");
  const view = $derived(semantic.frame?.view);
  const menu = $derived(
    view?.Menu && (view.Menu.Kind !== "" || view.Menu.Dialog) && !semantic.settingsMenu
      ? view.Menu
      : undefined,
  );
  const resume = $derived(view?.Resume);
  const panel = $derived(view?.Panel);
  const metricCount = $derived(panel?.Metrics?.exchanges.length ?? 0);
  const disabled = $derived(semantic.connection !== "open" || semantic.pending > 0);
  const groups = $derived.by(() => {
    const map = new Map<string, StatusField[]>();
    for (const field of panel?.Fields ?? [])
      map.set(field.Group, [...(map.get(field.Group) ?? []), field]);
    return [...map];
  });
  function choose(kind: number, choice: ChoiceID = { ID: 0, Name: "", Path: "" }) {
    if (!view || !menu) return;
    void semantic.send({
      menu: { Version: view.Version, Menu: menu.ID, Kind: kind, Choice: choice, Query: query },
      editor: semantic.metadata(),
    });
  }
  function selectResume(kind: number, thread = "") {
    if (!view || !resume) return;
    void semantic.send({
      resume: {
        Version: view.Version,
        Picker: resume.ID,
        Kind: kind,
        Thread: thread,
        Query: query,
        After: resume.PageAfter,
      },
    });
  }
  async function copy() {
    try {
      await navigator.clipboard.writeText(semantic.copied ?? "");
      semantic.copied = undefined;
    } catch {
      semantic.error = "The clipboard is unavailable here. Select the text above and copy it.";
    }
  }
  const row =
    "flex min-h-11 w-full items-center justify-between gap-3 rounded-card px-2.5 py-2 text-left text-ink hover:enabled:bg-active disabled:cursor-default";
  const search = "mb-3 flex gap-2";
  const foot = "mt-4 flex justify-end gap-2";
</script>

{#if menu && view}
  <Dialog
    title={menu.Title || menu.Setting || (menu.Kind === "manage" ? "Skills" : "Choose")}
    onclose={() => choose(1)}
  >
    {#if menu.Explanation}<p class="mb-3 text-muted">{menu.Explanation}</p>{/if}
    {#if menu.Kind === "manage"}
      <form
        class={search}
        onsubmit={(event) => {
          event.preventDefault();
          choose(3);
        }}
      >
        <label class="sr-only" for="menu-search">Search skills</label>
        <input id="menu-search" class={input} bind:value={query} placeholder="Search skills" />
        <Button type="submit" {disabled}>Search</Button>
      </form>
    {/if}
    {#if menu.Loading || menu.Saving}<p class="text-small text-muted" role="status">
        {menu.Saving ? "Saving…" : "Loading…"}
      </p>{/if}
    {#if menu.Problem}<Alert>{menu.Problem}</Alert>{/if}
    <ul class="grid gap-0.5">
      {#each menu.Choices as choice (JSON.stringify([choice.ID, choice.Name, choice.Path]))}
        {@const preferred = choice.ID === menu.Preferred.ID && choice.Name === menu.Preferred.Name}
        <li>
          <button
            type="button"
            class={[row, preferred && "bg-raised"]}
            disabled={disabled || menu.Saving}
            onclick={() => choose(2, { ID: choice.ID, Name: choice.Name, Path: choice.Path })}
          >
            <span class="grid min-w-0 gap-0.5">
              <span class="font-medium wrap-anywhere">{choice.Display || choice.Name}</span>
              {#if choice.Description}<span class="text-small text-muted">{choice.Description}</span
                >{/if}
            </span>
            {#if menu.Kind === "manage"}<Tag tone={choice.Enabled ? "live" : "neutral"}
                >{choice.Enabled ? "Enabled" : "Disabled"}</Tag
              >{:else if preferred}<Icon name="check" />{/if}
          </button>
        </li>
      {:else}
        {#if !menu.Loading}<li class="text-small text-muted">Nothing to choose.</li>{/if}
      {/each}
    </ul>
    {#if semantic.error}<Alert class="mt-3">{semantic.error}</Alert>{/if}
  </Dialog>
{:else if resume}
  <Dialog title="Resume saved thread" wide onclose={() => selectResume(4)}>
    <form
      class={search}
      onsubmit={(event) => {
        event.preventDefault();
        selectResume(1);
      }}
    >
      <label class="sr-only" for="resume-search">Search saved threads</label>
      <input
        id="resume-search"
        class={input}
        bind:value={query}
        placeholder="Search saved threads"
      />
      <Button type="submit" {disabled}>Search</Button>
    </form>
    <div class="{segments} mb-2 inline-flex" role="group" aria-label="Saved threads from">
      <button
        type="button"
        class={segment}
        aria-pressed={!resume.All}
        disabled={disabled || !resume.All}
        onclick={() => selectResume(2)}>This workspace</button
      >
      <button
        type="button"
        class={segment}
        aria-pressed={resume.All}
        disabled={disabled || resume.All}
        onclick={() => selectResume(2)}>All workspaces</button
      >
    </div>
    {#if resume.Loading}
      <div class="flex items-center gap-2 text-small text-muted" role="status">
        <Dot tone="running" />
        <span>Scanning saved threads{resume.Scanned ? ` · ${resume.Scanned} checked` : ""}…</span>
        <Button variant="quiet" {disabled} onclick={() => selectResume(5)}>Cancel scan</Button>
      </div>
    {/if}
    {#if resume.Problem}<Alert>{resume.Problem}</Alert>{/if}
    <ul class="grid gap-0.5">
      {#each resume.Rows as saved (saved.ID)}
        <li>
          <button type="button" class={row} {disabled} onclick={() => selectResume(3, saved.ID)}>
            <span class="grid min-w-0 gap-0.5">
              <span class="font-medium wrap-anywhere">{saved.Title || saved.ID}</span>
              <span class="flex min-w-0 gap-3 text-small text-muted">
                <span class="min-w-0 truncate text-left font-mono" dir="rtl" title={saved.Workspace}
                  ><bdi dir="ltr">{saved.Workspace}</bdi></span
                >
                {#if saved.Branch}<span class="shrink-0 font-mono whitespace-nowrap"
                    >{saved.Branch}</span
                  >{/if}
                <!-- Saved threads record seconds. -->
                {#if saved.Updated}<span class="shrink-0 whitespace-nowrap tabular-nums"
                    >{date(saved.Updated < 1e12 ? saved.Updated * 1000 : saved.Updated)}</span
                  >{/if}
              </span>
            </span>
          </button>
        </li>
      {:else}
        {#if !resume.Loading}<li class="px-2.5 py-4 text-small text-muted">
            No saved threads{query ? " match this search" : resume.All ? "" : " in this workspace"}.
          </li>{/if}
      {/each}
    </ul>
    {#if resume.CanPage}<Button class="mt-2" {disabled} onclick={() => selectResume(6)}
        >Load more</Button
      >{/if}
    {#if semantic.error}<Alert class="mt-3">{semantic.error}</Alert>{/if}
  </Dialog>
{:else if panel && view}
  <Dialog
    title={panel.Metrics ? "Session metrics" : "Session status"}
    wide
    onclose={() => semantic.send({ panel: { Version: view.Version, Panel: panel.ID, Kind: 1 } })}
  >
    {#if panel.Metrics}
      <div class="{segments} mb-4 flex flex-wrap" role="group" aria-label="Session metrics view">
        {#each ["Overview", "Transport", "Exchanges"] as tab, i (tab)}
          <button
            type="button"
            class={segment}
            aria-pressed={panel.MetricTab === i}
            {disabled}
            onclick={() =>
              semantic.send({
                panel: {
                  Version: view.Version,
                  Panel: panel.ID,
                  Kind: 3,
                  Tab: i,
                  Exchange: panel.MetricExchange,
                },
              })}>{tab}</button
          >
        {/each}
      </div>
      {#if panel.MetricTab === 2 && metricCount > 0}
        <div class="mb-3 flex flex-wrap items-center gap-2" aria-label="Retained exchange">
          <Button
            disabled={disabled || panel.MetricIndex === 0}
            onclick={() =>
              semantic.send({
                panel: {
                  Version: view.Version,
                  Panel: panel.ID,
                  Kind: 3,
                  Tab: 2,
                  Exchange: panel.Metrics!.exchanges[panel.MetricIndex - 1].sequence,
                },
              })}>Older</Button
          >
          <span class="text-small text-muted">{panel.MetricIndex + 1} of {metricCount}</span>
          <Button
            disabled={disabled || panel.MetricIndex >= metricCount - 1}
            onclick={() =>
              semantic.send({
                panel: {
                  Version: view.Version,
                  Panel: panel.ID,
                  Kind: 3,
                  Tab: 2,
                  Exchange: panel.Metrics!.exchanges[panel.MetricIndex + 1].sequence,
                },
              })}>Newer</Button
          >
        </div>
      {/if}
    {/if}
    {#each groups as [group, fields] (group)}
      <section class="not-first:mt-4">
        {#if group}<h3 class="mb-1.5 text-small font-semibold text-muted">{group}</h3>{/if}
        <dl class="grid grid-cols-[minmax(120px,1fr)_2fr] max-sm:grid-cols-1">
          {#each fields as field, i (i)}
            <dt class="py-[7px] text-muted">{field.Label}</dt>
            <dd
              class={[
                "grid gap-0.5 py-[7px] wrap-anywhere max-sm:pt-0",
                field.Alert && "text-negative",
              ]}
            >
              <span class={field.Path ? "font-mono" : undefined}>{field.Value}</span>
              {#if field.Detail}<span class="text-small text-muted">{field.Detail}</span>{/if}
            </dd>
          {/each}
        </dl>
      </section>
    {/each}
    <div class={foot}>
      <Button
        {disabled}
        onclick={() =>
          semantic.send({ panel: { Version: view.Version, Panel: panel.ID, Kind: 2 } })}
        >Refresh</Button
      >
    </div>
    {#if semantic.error}<Alert class="mt-3">{semantic.error}</Alert>{/if}
  </Dialog>
{:else if semantic.copied !== undefined}
  <Dialog title="Copy text" onclose={() => (semantic.copied = undefined)}>
    <textarea
      class="{input} min-h-45 resize-y font-mono max-sm:min-h-45"
      readonly
      value={semantic.copied}
      aria-label="Text to copy"></textarea>
    <div class={foot}>
      <Button variant="primary" icon="copy" onclick={copy}>Copy</Button>
    </div>
    {#if semantic.error}<Alert class="mt-3">{semantic.error}</Alert>{/if}
  </Dialog>
{/if}
