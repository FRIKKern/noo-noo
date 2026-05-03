<script lang="ts">
  import Settings from './Settings.svelte'
  let route = $state(window.location.hash.replace(/^#/, '') || '/')
  window.addEventListener('hashchange', () => {
    route = window.location.hash.replace(/^#/, '') || '/'
  })
</script>

{#if route === '/settings'}
  <Settings />
{:else}
  <p style="font-family:system-ui;padding:1rem">Use the menubar.</p>
{/if}
