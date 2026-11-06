<script>
  import { onMount } from 'svelte'
  import { CloudUpload, CircleCheck, CircleAlert, CirclePause, LoaderCircle } from '@lucide/svelte'
  import { get, post } from '../lib/api.js'
  import { live, toast } from '../lib/status.svelte.js'
  import { bytes, duration, n0, speed, shortDate, tm } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'

  let runs = $state(null)
  let error = $state('')
  async function load() {
    try { runs = await get('/api/history') } catch (e) { error = e.message }
  }
  onMount(() => { load(); const iv = setInterval(load, 10000); return () => clearInterval(iv) })

  const s = $derived(live.status)
  const a = $derived(s?.archive)
  const busy = $derived(['connecting', 'snapshot', 'copying', 'verifying', 'freeing'].includes(a?.phase))
  const frac = $derived(a?.total_bytes > 0 ? a.done_bytes / a.total_bytes : 0)
  const results = $derived({
    ok: { label: t('archive.complete'), icon: CircleCheck, color: 'var(--good)' },
    interrupted: { label: t('archive.interrupted'), icon: CirclePause, color: 'var(--warning)' },
    error: { label: t('archive.error'), icon: CircleAlert, color: 'var(--critical)' },
  })
  const totals = $derived(runs ? runs.reduce((t, r) => ({ files: t.files + r.files, bytes: t.bytes + r.bytes, freed: t.freed + r.freed }), { files: 0, bytes: 0, freed: 0 }) : null)

  async function archiveNow() {
    try { await post('/api/archive'); toast(t('toast.archive_started'), 'ok') }
    catch (e) { toast(e.message, 'error') }
  }
</script>

<div class="flex flex-wrap items-end gap-4">
  <div>
    <p class="eyebrow">NAS</p>
    <h1 class="mt-1 text-2xl font-semibold tracking-tight">{t('nav.archive')}</h1>
  </div>
  <button class="btn btn-primary ml-auto" disabled={!s?.wifi.enabled || busy} onclick={archiveNow}><CloudUpload size={16} />{t('action.archive_now')}</button>
</div>

{#if s}
  <section class="card mt-5 p-6">
    <div class="flex items-center gap-3">
      {#if busy}<LoaderCircle size={20} class="animate-spin" style="color: var(--accent)" />{/if}
      <h2 class="font-semibold">{busy ? t('archive.running') : t('archive.idle')}</h2>
      {#if a.pending > 0}<span class="badge ml-auto num" style="color: var(--warning)">{t('archive.n_pending', { n: n0(a.pending) })}</span>{/if}
    </div>
    <p class="mt-1 text-sm text-[var(--text-2)]">
      {busy ? tm(a.message) : s.wifi.enabled ? t('archive.repeats') : t('archive.starts_when')}
    </p>
    {#if a.phase === 'copying'}
      <div class="mt-4 h-2 overflow-hidden rounded-full bg-[var(--surface-2)]">
        <div class="h-full rounded-full transition-[width] duration-700" style="width: {frac * 100}%; background: linear-gradient(90deg, var(--accent), var(--accent-2))"></div>
      </div>
      <div class="num mt-2 flex flex-wrap gap-x-5 gap-y-1 text-sm text-[var(--text-2)]">
        <span>{t('archive.files_of', { done: n0(a.done_files), total: n0(a.total_files) })}</span>
        <span>{t('archive.bytes_of', { done: bytes(a.done_bytes), total: bytes(a.total_bytes) })}</span>
        <span>{speed(a.speed_bps)}</span>
        <span>{t('archive.remaining', { time: duration(a.eta_sec) })}</span>
      </div>
      {#if a.current}<p class="mt-2 truncate font-mono text-xs text-[var(--text-3)]">{a.current}</p>{/if}
    {/if}
  </section>
{/if}

{#if totals && runs.length}
  <section class="mt-4 grid grid-cols-3 gap-4">
    {#each [{ l: t('archive.k_files'), v: n0(totals.files) }, { l: t('archive.k_data'), v: bytes(totals.bytes) }, { l: t('archive.k_freed'), v: bytes(totals.freed) }] as k}
      <div class="card p-5">
        <p class="text-xs text-[var(--text-3)] sm:text-sm">{k.l}</p>
        <p class="num mt-1 text-xl font-semibold tracking-tight sm:text-2xl">{k.v}</p>
      </div>
    {/each}
  </section>
{/if}

<section class="card mt-4 overflow-hidden">
  <h2 class="px-6 pb-3 pt-5 font-semibold">{t('archive.history')}</h2>
  {#if error}
    <p class="px-6 pb-6 text-sm" style="color: var(--critical)">{error}</p>
  {:else if !runs}
    <div class="flex h-24 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
  {:else if runs.length === 0}
    <p class="px-6 pb-6 text-sm text-[var(--text-3)]">{t('archive.none_yet')}</p>
  {:else}
    <div class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="text-left text-xs text-[var(--text-3)]">
          <tr class="border-y border-[var(--border)]">
            <th class="px-6 py-2.5 font-medium">{t('archive.col_date')}</th><th class="px-3 py-2.5 font-medium">{t('archive.col_result')}</th>
            <th class="px-3 py-2.5 text-right font-medium">{t('archive.col_files')}</th><th class="px-3 py-2.5 text-right font-medium">{t('archive.col_copied')}</th>
            <th class="px-3 py-2.5 text-right font-medium">{t('archive.col_duration')}</th><th class="px-6 py-2.5 text-right font-medium">{t('archive.col_freed')}</th>
          </tr>
        </thead>
        <tbody>
          {#each runs as r}
            {@const res = r.result === 'ok' && r.files === 0 && !r.left_over ? { ...results.ok, label: t('archive.nothing_new'), color: 'var(--text-3)' } : results[r.result] ?? results.error}
            <tr class="border-b border-[var(--border)] last:border-0">
              <td class="num whitespace-nowrap px-6 py-3">{shortDate(new Date(r.start))}</td>
              <td class="px-3 py-3">
                <span class="inline-flex items-center gap-1.5" style="color: {res.color}"><res.icon size={15} /><span class="text-[var(--text)]">{res.label}</span></span>
                {#if tm(r.message) && !(r.result === 'ok' && r.files === 0 && !r.left_over)}<p class="mt-0.5 max-w-xs truncate text-xs text-[var(--text-3)]" title={tm(r.message)}>{tm(r.message)}</p>{/if}
                {#if r.left_over > 0}<p class="mt-0.5 text-xs text-[var(--text-3)]">{t('archive.n_pending', { n: n0(r.left_over) })}</p>{/if}
              </td>
              <td class="num px-3 py-3 text-right">{n0(r.files)}</td>
              <td class="num px-3 py-3 text-right">{bytes(r.bytes)}</td>
              <td class="num px-3 py-3 text-right">{duration((new Date(r.end) - new Date(r.start)) / 1000)}</td>
              <td class="num px-6 py-3 text-right">{r.freed ? bytes(r.freed) : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>
