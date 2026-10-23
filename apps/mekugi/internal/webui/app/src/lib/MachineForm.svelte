<script lang="ts">
  import Alert from "./ui/Alert.svelte";
  import { hub } from "./hub.svelte";
  import Button from "./ui/Button.svelte";
  import Field from "./ui/Field.svelte";
  import { input } from "./ui/styles";

  // Connects a machine and saves its credential in this browser.
  let { onadded, oncancel }: { onadded?: () => void; oncancel?: () => void } = $props();
  let label = $state("");
  let endpoint = $state("");
  let token = $state("");
  let error = $state<string>();
  async function onsubmit(event: SubmitEvent) {
    event.preventDefault();
    error = await hub.add(label, endpoint, token);
    if (error) return;
    label = endpoint = token = "";
    onadded?.();
  }
</script>

<form class="grid gap-3.5" {onsubmit}>
  <Field label="Machine name"
    ><input
      class={input}
      required
      bind:value={label}
      placeholder="Build box"
      autocomplete="off"
    /></Field
  >
  <Field label="Connector URL"
    ><input
      class="{input} font-mono"
      required
      type="url"
      bind:value={endpoint}
      placeholder="https://connector.example.com"
      autocomplete="off"
      spellcheck="false"
    /></Field
  >
  <Field label="Access token"
    ><input
      class="{input} font-mono"
      required
      type="password"
      bind:value={token}
      autocomplete="off"
      spellcheck="false"
    /></Field
  >
  <p class="text-small text-muted">
    On that machine, run <code class="text-ink wrap-anywhere"
      >mekugi connector --origin {location.origin}</code
    >. It listens on port 7778 and prints where its token file is. Beyond localhost, reach it over
    HTTPS. Machines and tokens are saved in this browser and reconnect after reload. Use a trusted
    browser; removing a machine deletes its saved token.
  </p>
  {#if error}<Alert>{error}</Alert>{/if}
  <div class="flex justify-end gap-2">
    {#if oncancel}<Button variant="quiet" onclick={oncancel}>Cancel</Button>{/if}
    <Button type="submit" variant="primary">Connect</Button>
  </div>
</form>
