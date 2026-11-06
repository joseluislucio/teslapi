<script>
  // Barras de intensidad (BLE/Wi-Fi) a partir de dBm.
  let { dbm = -100, active = true } = $props()
  const level = $derived(dbm >= -60 ? 4 : dbm >= -70 ? 3 : dbm >= -80 ? 2 : dbm >= -90 ? 1 : 0)
</script>

<span class="inline-flex items-end gap-[2px]" aria-label="{dbm} dBm">
  {#each [1, 2, 3, 4] as b}
    <span class="w-[3px] rounded-[1px]" style="height: {3 + b * 3}px; background: {active && b <= level ? 'var(--accent)' : 'var(--border-strong)'}"></span>
  {/each}
</span>
