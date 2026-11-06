<script>
  import { onMount } from 'svelte'
  import { Power, RotateCw, TriangleAlert, LoaderCircle } from '@lucide/svelte'
  import { get, post } from '../lib/api.js'
  import { live, toast } from '../lib/status.svelte.js'
  import { duration, n0, n1, clockTime } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'
  import BootChart from '../components/BootChart.svelte'
  import SdLife from '../components/SdLife.svelte'
  import Updates from '../components/Updates.svelte'

  const s = $derived(live.status)
  const sys = $derived(s?.system)

  let logs = $state([])
  let level = $state('all')
  async function loadLogs() { try { logs = await get('/api/logs') } catch {} }
  onMount(() => { loadLogs(); const iv = setInterval(loadLogs, 5000); return () => clearInterval(iv) })
  const shown = $derived(logs.filter((l) => level === 'all' || l.level === level).slice().reverse())
  const lvColor = { ERROR: 'var(--critical)', WARN: 'var(--warning)', INFO: 'var(--text-3)' }
  const lt = (iso) => clockTime(new Date(iso))

  // Qué acción espera confirmación: 'reboot' | 'poweroff' | null.
  let confirming = $state(null)
  async function run(kind) {
    try { await post(`/api/system/${kind}`); toast(t(kind === 'reboot' ? 'system.rebooting' : 'system.powering_off'), 'ok') }
    catch (e) { toast(e.message, 'error') }
    confirming = null
  }
</script>

<div class="flex flex-wrap items-end gap-4">
  <div>
    <p class="eyebrow">Raspberry Pi Zero 2 W</p>
    <h1 class="mt-1 text-2xl font-semibold tracking-tight">{t('nav.system')}</h1>
  </div>
  <div class="ml-auto flex gap-2">
    {#if confirming}
      <button class="btn" onclick={() => (confirming = null)}>{t('action.cancel')}</button>
      <button class="btn btn-danger" onclick={() => run(confirming)}><Power size={16} />{t(confirming === 'reboot' ? 'system.confirm_reboot' : 'system.confirm_poweroff')}</button>
    {:else}
      <button class="btn" onclick={() => (confirming = 'reboot')}><RotateCw size={16} />{t('system.reboot')}</button>
      <button class="btn" onclick={() => (confirming = 'poweroff')}><Power size={16} />{t('system.poweroff')}</button>
    {/if}
  </div>
</div>

{#if confirming === 'poweroff'}
  <div class="card mt-5 flex items-start gap-3 p-4" style="border-color: color-mix(in oklab, var(--warning) 40%, transparent)">
    <TriangleAlert size={18} style="color: var(--warning)" class="mt-0.5 shrink-0" />
    <p class="text-sm">{t('system.poweroff_warn')}</p>
  </div>
{/if}

{#if !sys}
  <div class="flex h-64 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
{:else}
  {#if sys.undervoltage || sys.uv_since_boot}
    <div class="card mt-5 flex items-start gap-3 p-4" style="border-color: color-mix(in oklab, var(--critical) 40%, transparent)">
      <TriangleAlert size={18} style="color: var(--critical)" class="mt-0.5 shrink-0" />
      <p class="text-sm">{sys.undervoltage ? t('system.uv_now') : t('system.uv_boot')}</p>
    </div>
  {/if}
  {#if sys.throttled || sys.throttled_since_boot}
    <div class="card mt-5 flex items-start gap-3 p-4" style="border-color: color-mix(in oklab, var(--serious) 40%, transparent)">
      <TriangleAlert size={18} style="color: var(--serious)" class="mt-0.5 shrink-0" />
      <p class="text-sm">{t('system.throttled_boot')}</p>
    </div>
  {/if}

  <section class="mt-5 grid grid-cols-2 gap-4 sm:grid-cols-4">
    {#each [
      { l: t('system.temp'), v: `${n1(sys.temp_c)} °C`, warn: sys.temp_c > 75, s: sys.temp_max_c > 0 ? t('system.temp_max', { v: n1(sys.temp_max_c) }) : '' },
      { l: t('system.mem'), v: `${n0(sys.mem_avail_mb)} / ${n0(sys.mem_total_mb)} MB` },
      { l: t('system.uptime'), v: duration(sys.uptime_sec) },
      { l: t('system.load'), v: n1(sys.load1) },
    ] as k}
      <div class="card p-5">
        <p class="text-sm text-[var(--text-3)]">{k.l}</p>
        <p class="num mt-1 text-xl font-semibold tracking-tight" style={k.warn ? 'color: var(--serious)' : ''}>{k.v}</p>
        {#if k.s}<p class="num mt-0.5 text-xs text-[var(--text-3)]">{k.s}</p>{/if}
      </div>
    {/each}
  </section>

  <section class="card mt-4 p-6">
    <div class="flex flex-wrap items-baseline gap-2">
      <h2 class="font-semibold">{t('system.boot_title')}</h2>
      <span class="text-sm text-[var(--text-3)]">{t('system.last_boots', { n: sys.boot_times?.length ?? 0 })}</span>
    </div>
    <div class="mt-4"><BootChart times={sys.boot_times ?? []} /></div>
  </section>

  <SdLife />
  <Updates />

  {#if sys.prev_alive}
    <div class="card mt-5 p-5">
      <h2 class="font-semibold">{t('system.prev_title')}</h2>
      <p class="mt-2 text-sm text-[var(--text-2)]">{t('system.prev_body', { dur: duration(sys.prev_alive.uptime_sec), at: clockTime(new Date(sys.prev_alive.at)) })}</p>
    </div>
  {/if}

  {#if sys.thermal?.length}
    <section class="card mt-4 p-6">
      <h2 class="font-semibold">{t('system.thermal_title')}</h2>
      <div class="mt-3 flex flex-wrap gap-2">
        {#each sys.thermal.slice(-12) as b}
          <span class="chip num !h-8" style={b.max_temp_c > 75 || b.throttled ? 'color: var(--serious)' : ''} title={b.throttled ? t('system.thermal_throttled') : ''}>{n1(b.max_temp_c)} °C{b.throttled ? ' ⚠' : ''}</span>
        {/each}
      </div>
    </section>
  {/if}

  <section class="card mt-4 overflow-hidden">
    <div class="flex flex-wrap items-center gap-2 px-6 pb-3 pt-5">
      <h2 class="mr-auto font-semibold">{t('system.log')}</h2>
      {#each [{ id: 'all', l: t('system.log_all') }, { id: 'WARN', l: t('system.log_warn') }, { id: 'ERROR', l: t('system.log_error') }] as f}
        <button class="chip !h-8" aria-pressed={level === f.id} onclick={() => (level = f.id)}>{f.l}</button>
      {/each}
    </div>
    <div class="max-h-[420px] overflow-y-auto border-t border-[var(--border)] px-6 py-3 font-mono text-[12.5px] leading-relaxed">
      {#each shown as l}
        <div class="flex gap-3">
          <span class="num shrink-0 text-[var(--text-3)]">{lt(l.time)}</span>
          <span class="w-11 shrink-0 font-semibold" style="color: {lvColor[l.level] ?? 'var(--text-3)'}">{l.level}</span>
          <span class="min-w-0 break-words text-[var(--text-2)]">{l.msg}</span>
        </div>
      {:else}
        <p class="text-[var(--text-3)]">{t('system.log_none')}</p>
      {/each}
    </div>
    <p class="border-t border-[var(--border)] px-6 py-3 text-xs text-[var(--text-3)]">{t('system.version', { v: sys.version })}</p>
  </section>
{/if}
