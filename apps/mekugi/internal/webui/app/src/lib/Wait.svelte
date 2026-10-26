<script lang="ts">
  import AgentName from "./AgentName.svelte";
  import Button from "./ui/Button.svelte";

  // A collaboration wait as its text reads in the terminal: what is waiting,
  // the agents it waits for with their status, the optional timeout and why
  // it finished. Names are plain inside another control.
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
    >
  {:else}<Button
      variant="quiet"
      size="sm"
      aria-expanded={expanded}
      onclick={() => (expanded = !expanded)}
      >{expanded ? "Show fewer agents" : `+${targets.length - 2} more`}</Button
    >{/if}{/if}{#if timeout}{" "}<span class="whitespace-nowrap text-muted"
    >(timeout {timeout})</span
  >{/if}{#if reason}{`: ${reason}`}{/if}
