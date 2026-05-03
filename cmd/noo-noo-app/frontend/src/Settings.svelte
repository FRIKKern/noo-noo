<script lang="ts">
  import { onMount } from 'svelte'
  import { loadConfig, saveConfig, openInEditor, type Config } from './lib/api'

  let cfg: Config | null = $state(null)
  let saving = $state(false)
  let saved = $state(false)

  onMount(async () => { cfg = await loadConfig() })

  async function onSave() {
    if (!cfg) return
    saving = true
    try { await saveConfig(cfg); saved = true; setTimeout(() => (saved = false), 1500) }
    finally { saving = false }
  }
</script>

<main class="wrap">
  <h1>Settings</h1>
  {#if !cfg}
    <p>Loading…</p>
  {:else}
    <section>
      <h2>Daemon</h2>
      <label>Daily scan hour
        <input type="number" min="0" max="23" bind:value={cfg.daemon.scan_hour} />
      </label>
    </section>

    <section>
      <h2>Notifications</h2>
      <label><input type="checkbox" bind:checked={cfg.notify.enabled} /> Enabled</label>
      <label>Min severity
        <select bind:value={cfg.notify.min_severity}>
          <option>low</option><option>medium</option><option>high</option>
        </select>
      </label>
    </section>

    <section>
      <h2>Heuristic: idle repos</h2>
      <label><input type="checkbox" bind:checked={cfg.heuristics.idle_repos.enabled} /> Enabled</label>
      <label>Min idle days
        <input type="number" min="1" bind:value={cfg.heuristics.idle_repos.min_idle_days} />
      </label>
      <label>Min node_modules size (bytes)
        <input type="number" min="0" bind:value={cfg.heuristics.idle_repos.min_node_modules_bytes} />
      </label>
    </section>

    <section>
      <h2>Heuristic: cache velocity</h2>
      <label><input type="checkbox" bind:checked={cfg.heuristics.cache_velocity.enabled} /> Enabled</label>
      <label>Growth multiplier
        <input type="number" step="0.1" min="1" bind:value={cfg.heuristics.cache_velocity.growth_multiplier} />
      </label>
      <label>Window days
        <input type="number" min="1" bind:value={cfg.heuristics.cache_velocity.window_days} />
      </label>
    </section>

    <section>
      <h2>Advanced</h2>
      <button type="button" onclick={openInEditor}>Edit raw config…</button>
    </section>

    <footer>
      <button onclick={onSave} disabled={saving}>{saving ? 'Saving…' : saved ? 'Saved' : 'Save'}</button>
    </footer>
  {/if}
</main>

<style>
  .wrap { max-width: 480px; margin: 1.5rem auto; padding: 0 1rem; font-family: system-ui; }
  section { border-bottom: 1px solid #ddd; padding: 0.8rem 0; }
  h2 { font-size: 0.95rem; margin: 0 0 0.4rem; color: #2c5f8d; }
  label { display: block; margin: 0.4rem 0; font-size: 0.9rem; }
  input[type="number"], select { display: block; margin-top: 0.2rem; padding: 0.3rem; }
  footer { position: sticky; bottom: 0; background: #fff; padding: 0.8rem 0; border-top: 1px solid #ddd; }
  button { padding: 0.5rem 1rem; font-size: 0.95rem; }
</style>
