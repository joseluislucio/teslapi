<script>
  // Busca dispositivos Bluetooth cercanos y deja elegir el que indica "estoy en casa".
  import { onDestroy } from 'svelte'
  import { Radar, Check } from '@lucide/svelte'
  import { get, post } from '../lib/api.js'
  import { toast } from '../lib/status.svelte.js'
  import { t } from '../lib/i18n.svelte.js'
  import Signal from './Signal.svelte'

  let { address = '', extras = [], onchoose, onadd } = $props()
  let disc = $state({ active: false, devices: [] })
  let timer
  async function discover() {
    try {
      await post('/api/ble/discover')
      disc.active = true
      clearInterval(timer)
      timer = setInterval(async () => {
        try { disc = await get('/api/ble/devices') } catch { /* reintenta */ }
        if (!disc.active) clearInterval(timer)
      }, 2000)
    } catch (e) { toast(e.message, 'error') }
  }
  onDestroy(() => clearInterval(timer))
</script>

<div class="rounded-2xl border border-[var(--border)] p-4">
  <div class="flex items-center gap-3">
    <Radar size={18} class={disc.active ? 'pulse' : ''} style="color: var(--accent)" />
    <div class="min-w-0">
      <p class="text-sm font-medium">{t('ble.title')}</p>
      <p class="text-xs text-[var(--text-3)]">{t('ble.hint')}</p>
    </div>
    <button class="btn ml-auto !h-9" disabled={disc.active} onclick={discover}>{disc.active ? t('ble.searching') : t('ble.search')}</button>
  </div>
  {#if disc.devices.length}
    <ul class="mt-3 max-h-72 divide-y divide-[var(--border)] overflow-y-auto">
      {#each disc.devices as d (d.address)}
        {@const chosen = d.address.toLowerCase() === address.toLowerCase()}
        {@const isExtra = extras.some((a) => (a || '').toLowerCase() === d.address.toLowerCase())}
        <li class="flex items-center gap-3 py-2.5">
          <Signal dbm={d.rssi} />
          <div class="min-w-0">
            <p class="truncate text-sm font-medium">{d.name || t('ble.unnamed')}</p>
            <p class="num text-xs text-[var(--text-3)]">{d.address} · {d.rssi} dBm</p>
          </div>
          <div class="ml-auto flex gap-2">
            <button class="chip !h-8" aria-pressed={chosen} onclick={() => onchoose(d)}>
              {#if chosen}<Check size={14} />{t('ble.chosen')}{:else}{t('ble.use')}{/if}
            </button>
            {#if onadd}
              <button class="chip !h-8" aria-pressed={isExtra} disabled={chosen || isExtra} onclick={() => onadd(d)}>
                {#if isExtra}<Check size={14} />{t('ble.extra')}{:else}{t('ble.add_extra')}{/if}
              </button>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>
