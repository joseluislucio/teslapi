<script>
  import { onMount, tick } from 'svelte'
  import { ChevronLeft, Play, Pause, RotateCcw, RotateCw, LayoutGrid, RectangleHorizontal, Download, Lock, MapPin, LoaderCircle, ExternalLink, VideoOff, CloudCheck, Crosshair } from '@lucide/svelte'
  import { get, mediaURL } from '../lib/api.js'
  import { carDate, dayLabel, time, bytes, reason, categories, catLabel, catShort, cameraName, cameraOrder, clockTime } from '../lib/format.js'
  import { t as tr } from '../lib/i18n.svelte.js'
  import MapView from '../components/MapView.svelte'

  let { source, dir, id } = $props()

  let ev = $state(null)
  let error = $state('')
  onMount(async () => {
    try {
      const list = await get(`/api/events?source=${source}`)
      ev = list.find((e) => e.dir === dir && e.id === id) ?? null
      if (!ev) error = tr('viewer.gone')
    } catch (e) { error = e.message }
  })

  const cams = $derived(ev ? cameraOrder.filter((c) => ev.cameras.includes(c)) : [])
  const segs = $derived(ev?.segments ?? [])

  // --- reproducción sincronizada ---
  let layout = $state((() => { try { return localStorage.getItem('viewer-layout') || 'focus' } catch { return 'focus' } })())
  let main = $state('front')
  let seg = $state(0)
  let t = $state(0)
  let playing = $state(false)
  let rate = $state(1)
  let durs = $state([])
  let pendingSeek = 0
  const vids = {}

  $effect(() => { try { localStorage.setItem('viewer-layout', layout) } catch {} })
  $effect(() => { if (cams.length && !cams.includes(main)) main = cams[0] })

  const segDur = (i) => durs[i] || 60
  const offset = (i) => { let s = 0; for (let k = 0; k < i; k++) s += segDur(k); return s }
  const total = $derived(segs.reduce((s, _, i) => s + segDur(i), 0))
  const globalT = $derived(offset(seg) + t)
  // Instante del aviso (timestamp de event.json) dentro del clip, como el reproductor de Tesla:
  // se localiza por la hora de cada minuto, no por la suma de duraciones (los minutos no son contiguos).
  const LEAD = 5 // s que se muestran antes del aviso
  const eventSeg = $derived.by(() => {
    const ts = carDate(ev?.meta?.timestamp)
    if (!ts || !segs.length) return null
    let i = -1
    for (let k = 0; k < segs.length; k++) { const s = carDate(segs[k].start); if (s && s <= ts) i = k }
    if (i < 0) return null
    return { i, o: (ts - carDate(segs[i].start)) / 1000 }
  })
  const eventG = $derived(eventSeg ? offset(eventSeg.i) + Math.min(eventSeg.o, segDur(eventSeg.i)) : null)
  async function goEvent() {
    if (!eventSeg) return
    let i = eventSeg.i, o = eventSeg.o - LEAD
    if (o < 0 && i > 0) { i--; o = Math.max(0, segDur(i) + o) } else o = Math.max(0, o)
    const act = active()
    if (i === seg && act.length && act[0].readyState >= 1) { for (const x of act) x.currentTime = o; t = o; return }
    pendingSeek = o; t = o
    if (i !== seg) { seg = i; await tick() }
  }
  let started = false
  $effect(() => { if (ev && eventSeg && !started) { started = true; goEvent() } })

  const masterCam = $derived(cams.includes('front') ? 'front' : cams[0])
  const active = () => Object.entries(vids).filter(([c, v]) => v && segs[seg]?.cameras[c]).map(([, v]) => v)

  function onTime(cam) {
    if (cam !== masterCam) return
    const m = vids[masterCam]
    t = m.currentTime
    // Las demás cámaras siguen a la frontal; se corrigen si se desvían.
    for (const v of active()) if (v !== m && Math.abs(v.currentTime - t) > 0.3) v.currentTime = t
  }
  async function onMeta(cam) {
    const v = vids[cam]
    v.playbackRate = rate
    if (cam === masterCam) {
      durs[seg] = v.duration
      if (pendingSeek) { for (const x of active()) x.currentTime = pendingSeek; pendingSeek = 0 }
    }
    if (playing) v.play().catch(() => {})
  }
  async function onEnded(cam) {
    if (cam !== masterCam) return
    if (seg < segs.length - 1) { seg++; t = 0 } else { playing = false }
  }
  function toggle() {
    playing = !playing
    for (const v of active()) playing ? v.play().catch(() => {}) : v.pause()
  }
  async function seek(g) {
    g = Math.max(0, Math.min(total - 0.1, g))
    let i = 0
    while (i < segs.length - 1 && g >= offset(i + 1)) i++
    const o = g - offset(i)
    if (i !== seg) { pendingSeek = o; seg = i; t = o; await tick() }
    else { for (const v of active()) v.currentTime = o; t = o }
  }
  function setRate(r) { rate = r; for (const v of active()) v.playbackRate = r }

  function onKey(e) {
    if (e.target.closest('input, select, textarea')) return
    if (e.code === 'Space') { e.preventDefault(); toggle() }
    if (e.key === 'ArrowRight') seek(globalT + 5)
    if (e.key === 'ArrowLeft') seek(globalT - 5)
  }

  const mmss = (s) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, '0')}`
  const clock = $derived.by(() => {
    const d = carDate(segs[seg]?.start)
    return d ? clockTime(new Date(d.getTime() + t * 1000)) : ''
  })
  const c = $derived(ev ? categories[ev.category] ?? categories.Other : null)
  const lat = $derived(parseFloat(ev?.meta?.est_lat))
  const lon = $derived(parseFloat(ev?.meta?.est_lon))
  const others = $derived(cams.filter((x) => x !== main))
</script>

<svelte:window onkeydown={onKey} />

<a href="#/clips" class="inline-flex items-center gap-1 text-sm text-[var(--text-2)] hover:text-[var(--text)]"><ChevronLeft size={16} />{tr('nav.clips')}</a>

{#if error}
  <div class="card mt-4 px-6 py-14 text-center text-[var(--text-2)]">{error}</div>
{:else if !ev}
  <div class="flex h-64 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
{:else}
  <div class="mt-3 flex flex-wrap items-end gap-3">
    <div class="min-w-0">
      <p class="eyebrow flex items-center gap-2"><span class="h-2 w-2 rounded-full" style="background: {c.color}"></span>{catLabel(ev.category)}</p>
      <h1 class="mt-1 text-2xl font-semibold tracking-tight">
        {reason(ev.meta?.reason) ?? (ev.category === 'RecentClips' ? tr('viewer.trip') : catShort(ev.category))}
      </h1>
      <p class="mt-1 text-sm text-[var(--text-2)]">
        {dayLabel(carDate(ev.start))} · {time(carDate(ev.start))}–{time(carDate(ev.end))}
        {#if ev.meta?.city} · {[ev.meta.street, ev.meta.city].filter(Boolean).join(', ')}{/if}
      </p>
    </div>
    {#if !ev.encrypted}
      <div class="ml-auto inline-flex rounded-xl border border-[var(--border)] bg-[var(--surface)] p-1">
        <button class="flex h-8 w-9 items-center justify-center rounded-lg {layout === 'focus' ? 'bg-[var(--surface-2)]' : 'text-[var(--text-3)]'}"
          title={tr('viewer.focus')} aria-pressed={layout === 'focus'} onclick={() => (layout = 'focus')}><RectangleHorizontal size={16} /></button>
        <button class="flex h-8 w-9 items-center justify-center rounded-lg {layout === 'grid' ? 'bg-[var(--surface-2)]' : 'text-[var(--text-3)]'}"
          title={tr('viewer.grid')} aria-pressed={layout === 'grid'} onclick={() => (layout = 'grid')}><LayoutGrid size={16} /></button>
      </div>
    {/if}
  </div>

  <div class="mt-5 grid gap-5 lg:grid-cols-[1fr_320px]">
    <div class="min-w-0">
      {#if ev.encrypted}
        <div class="card flex flex-col items-center gap-4 px-6 py-16 text-center">
          <span class="flex h-14 w-14 items-center justify-center rounded-2xl bg-[var(--surface-2)]"><Lock size={26} /></span>
          <h2 class="text-lg font-semibold">{tr('viewer.encrypted')}</h2>
          <p class="max-w-md text-sm text-[var(--text-2)]">
            {tr('viewer.encrypted_text')}
          </p>
          <a class="btn" href="https://dashcam.tesla.com" target="_blank" rel="noreferrer">{tr('viewer.open_dashcam')}<ExternalLink size={14} /></a>
        </div>
      {:else}
        {#key seg}
          {#snippet video(cam, big)}
            {#if big}
              <div class="relative overflow-hidden rounded-2xl bg-black">{@render inner(cam, big)}</div>
            {:else}
              <button class="relative block w-full overflow-hidden rounded-2xl bg-black outline-offset-2"
                onclick={() => (main = cam)} aria-label={tr('viewer.enlarge', { cam: cameraName(cam) })}>{@render inner(cam, big)}</button>
            {/if}
          {/snippet}
          {#snippet inner(cam, big)}
            {@const rel = segs[seg]?.cameras[cam]}
              {#if rel}
                <video bind:this={vids[cam]} src={mediaURL(source, rel)} class="aspect-[4/3] w-full object-contain" muted playsinline preload="auto"
                  ontimeupdate={() => onTime(cam)} onloadedmetadata={() => onMeta(cam)} onended={() => onEnded(cam)}></video>
              {:else}
                <div class="flex aspect-[4/3] w-full flex-col items-center justify-center gap-2 text-xs text-white/50"><VideoOff size={20} />{tr('viewer.no_video')}</div>
              {/if}
              <span class="badge absolute left-2.5 top-2.5 !bg-black/55 !text-white backdrop-blur">{cameraName(cam)}</span>
              {#if big}<span class="badge num absolute right-2.5 top-2.5 !bg-black/55 !text-white backdrop-blur">{clock}</span>{/if}
          {/snippet}
          {#if layout === 'grid'}
            <div class="grid grid-cols-2 gap-2">
              {#each cams as cam (cam)}{@render video(cam, true)}{/each}
            </div>
          {:else}
            {@render video(main, true)}
            <div class="mt-2 grid gap-2" style="grid-template-columns: repeat({Math.max(others.length, 1)}, minmax(0, 1fr))">
              {#each others as cam (cam)}{@render video(cam, false)}{/each}
            </div>
          {/if}
        {/key}

        <!-- Controles -->
        <div class="card mt-3 flex flex-wrap items-center gap-3 px-4 py-3">
          <button class="flex h-10 w-10 items-center justify-center rounded-full"
            style="background: var(--accent); color: var(--accent-ink)" onclick={toggle} aria-label={playing ? tr('viewer.pause') : tr('viewer.play')}>
            {#if playing}<Pause size={18} fill="currentColor" />{:else}<Play size={18} fill="currentColor" class="ml-0.5" />{/if}
          </button>
          <button class="text-[var(--text-2)] hover:text-[var(--text)]" onclick={() => seek(globalT - 10)} aria-label={tr('viewer.back10')}><RotateCcw size={18} /></button>
          <button class="text-[var(--text-2)] hover:text-[var(--text)]" onclick={() => seek(globalT + 10)} aria-label={tr('viewer.fwd10')}><RotateCw size={18} /></button>
          <span class="num text-sm text-[var(--text-2)]">{mmss(globalT)} / {mmss(total)}</span>
          {#if eventSeg}
            <button class="chip !h-8 !px-2.5 text-xs" onclick={goEvent} title={tr('viewer.go_event')}><Crosshair size={14} />{tr('viewer.go_event')}</button>
          {/if}
          <div class="relative order-last w-full sm:order-none sm:flex-1">
            <input type="range" min="0" max={total} step="0.1" value={globalT} oninput={(e) => seek(+e.currentTarget.value)}
              class="h-1.5 w-full cursor-pointer accent-[var(--accent)]" aria-label={tr('viewer.position')} />
            {#if eventG != null && total > 0}
              <span class="pointer-events-none absolute top-1/2 h-4 w-[3px] -translate-x-1/2 -translate-y-1/2 rounded-full bg-[var(--text)]"
                style="left: {(eventG / total) * 100}%" aria-hidden="true"></span>
            {/if}
          </div>
          <select class="input !h-9 !w-auto !py-0 text-sm" value={rate} onchange={(e) => setRate(+e.currentTarget.value)} aria-label={tr('viewer.speed')}>
            {#each [0.5, 1, 2, 4] as r}<option value={r}>{r}×</option>{/each}
          </select>
        </div>
      {/if}
    </div>

    <aside class="flex flex-col gap-4">
      {#if isFinite(lat) && isFinite(lon)}
        <div class="card p-2">
          <MapView {lat} {lon} label={ev.meta.city} />
          <p class="flex items-center gap-1.5 px-2 pb-1 pt-2.5 text-sm text-[var(--text-2)]"><MapPin size={14} />{[ev.meta.street, ev.meta.city].filter(Boolean).join(', ')}</p>
        </div>
      {/if}
      <div class="card p-5">
        <p class="eyebrow">{tr('viewer.details')}</p>
        <dl class="mt-3 grid grid-cols-2 gap-y-3 text-sm">
          <dt class="text-[var(--text-3)]">{tr('viewer.duration')}</dt><dd class="num text-right">{segs.length} min</dd>
          <dt class="text-[var(--text-3)]">{tr('viewer.cameras')}</dt><dd class="text-right">{ev.cameras.length}</dd>
          <dt class="text-[var(--text-3)]">{tr('viewer.size')}</dt><dd class="num text-right">{bytes(ev.bytes)}</dd>
          <dt class="text-[var(--text-3)]">{tr('viewer.origin')}</dt><dd class="text-right">{source === 'pi' ? tr('viewer.origin_drive') : 'NAS'}</dd>
          {#if source === 'pi'}
            <dt class="text-[var(--text-3)]">{tr('clips.in_nas')}</dt>
            <dd class="flex items-center justify-end gap-1 text-right" style="color: {ev.archived ? 'var(--good)' : 'var(--text-2)'}">
              {#if ev.archived}<CloudCheck size={14} />{tr('viewer.yes')}{:else}{tr('viewer.pending')}{/if}
            </dd>
          {/if}
        </dl>
      </div>
      {#if !ev.encrypted && segs[seg]}
        <div class="card p-5">
          <p class="eyebrow">{tr('viewer.download')}</p>
          <div class="mt-3 grid grid-cols-2 gap-2">
            {#each cams.filter((x) => segs[seg].cameras[x]) as cam}
              <a class="btn !h-9 justify-center !px-2 text-[13px]" href={mediaURL(source, segs[seg].cameras[cam]) + '?download'}>
                <Download size={14} />{cameraName(cam)}
              </a>
            {/each}
          </div>
        </div>
        {#if segs.length > 1}
          <div class="card p-5">
            <p class="eyebrow">{tr('viewer.minutes')}</p>
            <div class="mt-3 flex flex-wrap gap-1.5">
              {#each segs as sg, i}
                <button class="chip num !h-8 !px-2.5 text-xs" aria-pressed={i === seg} onclick={() => seek(offset(i))}>{time(carDate(sg.start))}</button>
              {/each}
            </div>
          </div>
        {/if}
      {/if}
    </aside>
  </div>
{/if}
