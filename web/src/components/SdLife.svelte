<script>
  // Vida útil de la microSD: lectura real de la tarjeta si la da, y si no una estimación por
  // los bytes escritos. Los supuestos (tipo de tarjeta) se eligen aquí y valen para cualquier modelo.
  import { onMount } from 'svelte'
  import { HardDrive, LoaderCircle, Save } from '@lucide/svelte'
  import { get, put } from '../lib/api.js'
  import { toast } from '../lib/status.svelte.js'
  import { bytes, n0, n1, shortDate } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'

  let sd = $state(null)
  let cfg = $state(null)
  let draft = $state({ profile: 'consumer', pe_cycles: 1000, waf: 2 })
  let saving = $state(false)

  const presets = { consumer: [1000, 2], endurance: [3000, 1.5], industrial: [10000, 1.3] }

  async function load() {
    try { sd = await get('/api/system/sdcard') } catch { /* se reintenta */ }
  }
  onMount(async () => {
    load()
    try { cfg = await get('/api/config'); draft = { profile: cfg.sdcard.profile || 'consumer', pe_cycles: cfg.sdcard.pe_cycles, waf: cfg.sdcard.waf } } catch {}
    const iv = setInterval(load, 60000)
    return () => clearInterval(iv)
  })

  function pick(p) {
    draft.profile = p
    if (presets[p]) [draft.pe_cycles, draft.waf] = presets[p]
  }
  const dirty = $derived(cfg && (draft.profile !== (cfg.sdcard.profile || 'consumer') || (draft.profile === 'custom' && (draft.pe_cycles !== cfg.sdcard.pe_cycles || draft.waf !== cfg.sdcard.waf))))
  async function save() {
    saving = true
    try {
      cfg.sdcard = { profile: draft.profile, pe_cycles: +draft.pe_cycles, waf: +draft.waf }
      cfg = await put('/api/config', cfg)
      await load()
      toast(t('sd.saved'), 'ok')
    } catch (e) { toast(e.message, 'error') }
    finally { saving = false }
  }

  const span = (days) => (days >= 730 ? t('sd.years', { v: n1(days / 365) }) : t('sd.days', { v: n0(days) }))
  const level = $derived(!sd ? 'ok' : sd.remaining_pct < 10 || sd.eol === 'urgent' ? 'critical' : sd.remaining_pct < 25 || sd.eol === 'warning' ? 'serious' : 'ok')
  const color = $derived(level === 'critical' ? 'var(--critical)' : level === 'serious' ? 'var(--serious)' : 'var(--good)')
  const maxDay = $derived(sd?.days?.length ? Math.max(...sd.days.slice(-14).map((d) => d.bytes), 1) : 1)
</script>

<section class="card mt-4 p-6">
  <div class="flex flex-wrap items-center gap-3">
    <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-[var(--surface-2)]"><HardDrive size={19} /></span>
    <div class="min-w-0">
      <h2 class="font-semibold">{t('sd.title')}</h2>
      {#if sd?.present}
        <p class="num truncate text-sm text-[var(--text-2)]">{[sd.vendor, sd.model].filter(Boolean).join(' ')} · {bytes(sd.capacity_bytes)} · {sd.device}{sd.date ? ` · ${sd.date}` : ''}</p>
      {/if}
    </div>
    {#if sd?.present}
      <span class="chip num ml-auto !h-8" title={sd.source === 'reported' ? t('sd.note_reported') : t('sd.note_estimated')}>{sd.source === 'reported' ? t('sd.reported') : t('sd.estimated')}</span>
    {/if}
  </div>

  {#if !sd}
    <div class="flex h-24 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
  {:else if !sd.present}
    <p class="mt-4 text-sm text-[var(--text-3)]">{t('sd.not_found')}</p>
  {:else}
    <div class="mt-5 flex items-end gap-3">
      <p class="num text-4xl font-semibold tracking-tight" style="color: {color}">{n1(sd.remaining_pct)} %</p>
      <p class="mb-1.5 text-sm text-[var(--text-3)]">{t('sd.remaining')}</p>
    </div>
    <div class="mt-3 h-3 w-full overflow-hidden rounded-[4px] bg-[var(--surface-2)]" role="img" aria-label={t('sd.remaining') + ' ' + n1(sd.remaining_pct) + ' %'}>
      <div class="h-full rounded-[4px]" style="width: {Math.max(sd.remaining_pct, 0.6)}%; background: {color}"></div>
    </div>
    {#if sd.eol === 'warning' || sd.eol === 'urgent'}
      <p class="mt-3 text-sm" style="color: {color}">{t(`sd.eol_${sd.eol}`)}</p>
    {/if}

    <div class="mt-5 grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
      {#each [
        { l: t('sd.stat_left'), v: sd.days_left ? span(sd.days_left) : t('sd.measuring') },
        { l: t('sd.stat_written'), v: bytes(sd.written_bytes) },
        { l: t('sd.stat_rate'), v: sd.bytes_per_day > 0 ? t('sd.per_day', { v: bytes(sd.bytes_per_day) }) : '—' },
        { l: t('sd.stat_iops'), v: sd.write_iops > 0 ? n1(sd.write_iops) + ' /s' : '—' },
        { l: t('sd.stat_avg'), v: sd.avg_write_kib > 0 ? n0(sd.avg_write_kib) + ' KiB' : '—' },
        { l: t('sd.stat_since'), v: shortDate(new Date(sd.tracked_since)) },
      ] as k}
        <div>
          <p class="text-xs text-[var(--text-3)]">{k.l}</p>
          <p class="num mt-0.5 text-sm font-semibold">{k.v}</p>
        </div>
      {/each}
    </div>

    {#if sd.days?.length > 1}
      <div class="mt-5">
        <p class="mb-2 text-xs text-[var(--text-3)]">{t('sd.last_days')}</p>
        <div class="flex h-14 items-end gap-1" role="img" aria-label={t('sd.last_days')}>
          {#each sd.days.slice(-14) as d}
            <div class="min-w-[4px] max-w-7 flex-1 rounded-[3px] bg-[var(--series-1)]" style="height: {Math.max((d.bytes / maxDay) * 100, 4)}%" title="{d.date}: {bytes(d.bytes)}"></div>
          {/each}
        </div>
      </div>
    {/if}

    <p class="mt-5 text-xs text-[var(--text-3)]">{sd.source === 'reported' ? t('sd.note_reported') : t('sd.note_estimated')}</p>

    <details class="mt-4 rounded-2xl border border-[var(--border)] p-4">
      <summary class="cursor-pointer text-sm font-medium">{t('sd.assumptions')}</summary>
      <div class="mt-4 flex flex-wrap gap-2">
        {#each ['consumer', 'endurance', 'industrial', 'custom'] as p}
          <button class="chip" aria-pressed={draft.profile === p} onclick={() => pick(p)}>{t(`sd.profile_${p}`)}</button>
        {/each}
      </div>
      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <label>
          <span class="mb-1.5 block text-sm font-medium">{t('sd.pe')}</span>
          <span class="mb-2 block text-xs text-[var(--text-3)]">{t('sd.pe_hint')}</span>
          <input class="input num" type="number" min="100" max="200000" disabled={draft.profile !== 'custom'} bind:value={draft.pe_cycles} />
        </label>
        <label>
          <span class="mb-1.5 block text-sm font-medium">{t('sd.waf')}</span>
          <span class="mb-2 block text-xs text-[var(--text-3)]">{t('sd.waf_hint')}</span>
          <input class="input num" type="number" min="1" max="10" step="0.1" disabled={draft.profile !== 'custom'} bind:value={draft.waf} />
        </label>
      </div>
      <p class="num mt-3 text-xs text-[var(--text-3)]">{t('sd.endurance', { v: bytes(sd.endurance_bytes) })}</p>
      <button class="btn btn-primary mt-4" disabled={!dirty || saving} onclick={save}>
        {#if saving}<LoaderCircle size={16} class="animate-spin" />{:else}<Save size={16} />{/if}{t('settings.save')}
      </button>
    </details>
  {/if}
</section>
