<script>
  // Actualizaciones de la Pi, en dos botones: software y firmware (+ kernel). Antes de aplicar
  // enseña qué cambia y cuánto tardará; mientras se aplica, el progreso real y el tiempo que queda.
  import { onMount } from 'svelte'
  import { Download, RefreshCw, LoaderCircle, TriangleAlert, CircleCheck, RotateCw, Package, Cpu } from '@lucide/svelte'
  import { get, post } from '../lib/api.js'
  import { toast } from '../lib/status.svelte.js'
  import { bytes, duration, n0, time, tm } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'

  let st = $state(null)
  let confirming = $state(null) // 'software' | 'firmware' | null
  let busyCall = $state(false)
  let timer

  const active = $derived(st && ((st.phase === 'checking' || st.phase === 'running') || (st.job && !st.job.done)))
  async function load() {
    try { st = await get('/api/system/updates') } catch { /* sin permisos o sin API: no se enseña */ }
    schedule()
  }
  function schedule() {
    clearTimeout(timer)
    timer = setTimeout(load, active ? 1500 : 30000)
  }
  onMount(() => { load(); return () => clearTimeout(timer) })

  async function call(path, body) {
    busyCall = true
    try { await post(path, body); await load() }
    catch (e) { toast(e.message, 'error') }
    finally { busyCall = false }
  }
  const check = () => call('/api/system/updates/check')
  async function apply(g) { confirming = null; await call('/api/system/updates/run', { group: g }) }
  async function reboot() {
    try { await post('/api/system/reboot'); toast(t('system.rebooting'), 'ok') } catch (e) { toast(e.message, 'error') }
  }

  const groups = [
    { id: 'software', icon: Package },
    { id: 'firmware', icon: Cpu },
  ]
  const job = $derived(st?.job)
  const running = $derived(job && !job.done)
  const STALE_MS = 30 * 24 * 3600 * 1000
  const stale = $derived(st && (!st.checked_at || st.checked_at.startsWith('0001') || Date.now() - new Date(st.checked_at) > STALE_MS))
  const diskText = (d) => (d >= 0 ? '+' : '−') + bytes(Math.abs(d))
</script>

{#snippet jobPanel(j)}
  <div class="mt-4 rounded-2xl border border-[var(--border)] p-4">
    {#if !j.done}
      <div class="flex items-center gap-3">
        <LoaderCircle size={16} class="animate-spin shrink-0" style="color: var(--accent)" />
        <p class="min-w-0 truncate text-sm font-medium">{t(`upd.step_${j.step}`)}{j.detail ? ` · ${j.detail}` : ''}</p>
        <p class="num ml-auto shrink-0 text-sm font-semibold">{n0(j.percent)} %</p>
      </div>
      <div class="mt-3 h-3 w-full overflow-hidden rounded-[4px] bg-[var(--surface-2)]" role="progressbar" aria-valuenow={Math.round(j.percent)} aria-valuemin="0" aria-valuemax="100">
        <div class="h-full rounded-[4px] bg-[var(--accent)] transition-[width] duration-500" style="width: {Math.max(j.percent, 1)}%"></div>
      </div>
      <p class="num mt-2 text-xs text-[var(--text-3)]">{t('upd.eta_left', { t: duration(j.eta_sec) })} · {t('upd.dont_unplug')}</p>
    {:else if j.error}
      <div class="flex items-start gap-3">
        <TriangleAlert size={18} style="color: var(--critical)" class="mt-0.5 shrink-0" />
        <div class="min-w-0"><p class="text-sm font-medium">{t('upd.failed')}</p>
          <p class="mt-1 whitespace-pre-wrap break-words text-sm text-[var(--text-2)]">{tm(j.error)}</p></div>
      </div>
    {:else}
      <div class="flex items-start gap-3">
        <CircleCheck size={18} style="color: var(--accent)" class="mt-0.5 shrink-0" />
        <div class="min-w-0">
          <p class="text-sm font-medium">{t('upd.done_ok', { n: j.applied, t: duration((new Date(j.ended_at) - new Date(j.started_at)) / 1000) })}</p>
          {#if j.root_writable}
            <p class="mt-1 text-sm" style="color: var(--warning)">{t('upd.root_rw')}</p>
          {/if}
          {#if j.reboot_required}
            <p class="mt-1 text-sm text-[var(--text-2)]">{t('upd.done_reboot')}</p>
            <button class="btn mt-3" onclick={reboot}><RotateCw size={16} />{t('upd.reboot_now')}</button>
          {/if}
        </div>
      </div>
    {/if}
    {#if j.log?.length}
      <details class="mt-3">
        <summary class="cursor-pointer text-xs text-[var(--text-3)]">{t('upd.log')}</summary>
        <pre class="mt-2 max-h-48 overflow-auto rounded-xl bg-[var(--surface-2)] p-3 font-mono text-[12px] leading-relaxed text-[var(--text-2)]">{j.log.slice(-40).join('\n')}</pre>
      </details>
    {/if}
  </div>
{/snippet}

<section class="card mt-4 p-6">
  <div class="flex flex-wrap items-center gap-3">
    <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-[var(--surface-2)]"><Download size={19} /></span>
    <div class="min-w-0">
      <h2 class="font-semibold">{t('upd.title')}</h2>
      <p class="text-sm text-[var(--text-2)]">{t('upd.text')}</p>
    </div>
    <div class="ml-auto flex items-center gap-3">
      <span class="num text-xs text-[var(--text-3)]">{st?.checked_at && !st.checked_at.startsWith('0001') ? t('upd.checked_at', { at: time(new Date(st.checked_at)) }) : t('upd.never')}</span>
      <button class="btn" disabled={!st || active || busyCall} onclick={check}>
        {#if st?.phase === 'checking'}<LoaderCircle size={16} class="animate-spin" />{t('upd.checking')}{:else}<RefreshCw size={16} />{t('upd.check')}{/if}
      </button>
    </div>
  </div>

  {#if stale && !active}
    <p class="mt-4 flex items-start gap-2 text-sm text-[var(--text-2)]"><TriangleAlert size={16} class="mt-0.5 shrink-0" style="color: var(--warning)" />{t('upd.stale')}</p>
  {/if}
  {#if st && !st.online}
    <p class="mt-4 flex items-start gap-2 text-sm text-[var(--text-2)]"><TriangleAlert size={16} class="mt-0.5 shrink-0" style="color: var(--warning)" />{t('upd.offline')}</p>
  {/if}
  {#if st?.error}
    <p class="mt-4 flex items-start gap-2 text-sm" style="color: var(--critical)"><TriangleAlert size={16} class="mt-0.5 shrink-0" /><span class="whitespace-pre-wrap break-words">{tm(st.error)}</span></p>
  {/if}

  <div class="mt-5 grid gap-4 lg:grid-cols-2">
    {#each groups as g}
      {@const p = st?.plans?.[g.id]}
      {@const mine = job && job.group === g.id}
      <div class="rounded-2xl border border-[var(--border)] p-5">
        <div class="flex items-center gap-2">
          <g.icon size={17} />
          <h3 class="font-semibold">{t(`upd.${g.id}`)}</h3>
          {#if p && !p.packages?.length}<span class="chip ml-auto !h-7"><CircleCheck size={13} />{t('upd.uptodate')}</span>{/if}
        </div>
        <p class="mt-1 text-xs text-[var(--text-3)]">{t(`upd.${g.id}_text`)}</p>

        {#if !p}
          <p class="mt-4 text-sm text-[var(--text-3)]">{st?.phase === 'checking' ? t('upd.checking') : t('upd.never')}</p>
        {:else if p.packages?.length}
          <p class="num mt-4 text-sm font-medium">{t('upd.summary', { n: p.packages.length, dl: bytes(p.download_bytes), disk: diskText(p.disk_delta) })}</p>
          <p class="num mt-1 text-sm text-[var(--text-2)]">{t('upd.eta', { t: duration(p.eta_sec) })} <span class="text-[var(--text-3)]">({t('upd.eta_split', { dl: duration(p.download_sec), inst: duration(p.install_sec) })})</span></p>
          {#if p.notes?.length}
            <div class="mt-3 flex flex-wrap gap-2">
              {#each p.notes as n}<span class="chip !h-7 !text-xs">{t(`upd.note_${n}`)}</span>{/each}
            </div>
          {/if}
          <details class="mt-3">
            <summary class="cursor-pointer text-sm text-[var(--text-2)]">{t('upd.list', { n: p.packages.length })}</summary>
            <ul class="num mt-2 max-h-44 overflow-y-auto text-xs leading-relaxed text-[var(--text-2)]">
              {#each p.packages as k}
                <li class="flex gap-2"><span class="min-w-0 truncate font-medium">{k.name}</span>
                  <span class="ml-auto shrink-0 text-[var(--text-3)]">{k.from ? `${k.from} → ${k.to}` : `${t('upd.new_pkg')} ${k.to}`}</span></li>
              {/each}
            </ul>
          </details>
          {#if confirming === g.id}
            <div class="mt-4 rounded-xl p-3 text-sm" style="background: color-mix(in oklab, var(--warning) 12%, transparent)">
              <p class="flex items-start gap-2"><TriangleAlert size={16} class="mt-0.5 shrink-0" style="color: var(--warning)" />{t('upd.warn')}</p>
              <div class="mt-3 flex gap-2">
                <button class="btn" onclick={() => (confirming = null)}>{t('action.cancel')}</button>
                <button class="btn btn-primary" onclick={() => apply(g.id)}><Download size={16} />{t('upd.confirm')}</button>
              </div>
            </div>
          {:else}
            <button class="btn btn-primary mt-4" disabled={active || busyCall || !st.online} onclick={() => (confirming = g.id)}>
              <Download size={16} />{t(`upd.update_${g.id}`)}
            </button>
          {/if}
        {/if}
        {#if mine}{@render jobPanel(job)}{/if}
      </div>
    {/each}
  </div>
</section>
