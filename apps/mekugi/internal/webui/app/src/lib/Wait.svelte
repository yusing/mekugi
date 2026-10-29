<script lang="ts">
  import AgentName from "./AgentName.svelte";

  // A collaboration wait: what is waiting, the agents it waits for with their
  // status, the optional timeout and why it finished. Names are plain inside
  // another control.
  let {
    text,
    targets,
    timeout,
    reason,
    owner,
    plain = false,
  }: {
    text: string;
    targets: { name: string; status?: string }[];
    timeout?: string;
    reason?: string;
    owner: string;
    plain?: boolean;
  } = $props();

  let expanded = $state(false);
  const shown = $derived(!plain && expanded ? targets : targets.slice(0, 2));
</script>

<span>{text}</span>{#each shown as target, i (i)}{i ? ", " : " "}<span class="whitespace-nowrap"
    >{#if target.name === "all"}all{:else}<AgentName
        name={target.name}
        current={owner}
        {plain}
      />{/if}{#if target.status}<span class="text-muted">: {target.status}</span>{/if}</span
  >{/each}{#if targets.length > 2}{" "}{#if plain}<span class="text-muted"
      >+{targets.length - 2} more</span
    >{:else}<!-- An inline text control, so the row keeps its line height. --><button
      type="button"
      class="-mx-1 -my-0.5 rounded-chip px-1 py-0.5 font-medium whitespace-nowrap text-ink hover:bg-active max-sm:-my-3.5 max-sm:py-3.5 pointer-coarse:-my-3.5 pointer-coarse:py-3.5"
      aria-expanded={expanded}
      onclick={() => (expanded = !expanded)}
      >{expanded ? "Show fewer agents" : `+${targets.length - 2} more`}</button
    >{/if}{/if}{#if timeout}{" "}<span class="whitespace-nowrap text-muted"
    >(timeout {timeout})</span
  >{/if}{#if reason}<span class="text-muted">{` ·\u00a0${reason}`}</span>{/if}
