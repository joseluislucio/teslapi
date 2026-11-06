<script>
  import { Search, Lock, CloudCheck, MapPin, Shield, Bookmark, Route, Flag, LoaderCircle, RefreshCw, Film, HardDrive, Server } from '@lucide/svelte'
  import { get, post, mediaURL } from '../lib/api.js'
  import { toast } from '../lib/status.svelte.js'
  import { carDate, dayLabel, time, duration, bytes, reason, categories, catShort } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'

  const saved = (() => { try { return JSON.parse(localStorage.getItem('clips-filter') || '{}') } catch { return {} } })()
  let source = $state(saved.source || 'pi')
  let cat = $state(saved.cat || 'all')
  let q = $state('')
  let events = $state(null)
  let error = $state('')
  let loading = $state(false)

  $effect(() => {
    try { localStorage.setItem('clips-filter', JSON.stringify({ source, cat })) } catch {}
  })

  async function load() {
    loading = true
    error = ''
    try { events = await get(`/api/events?source=${source}`) }
    catch (e) { error = e.message; events = null }
    finally { loading = false }
  }
  $effect(() => { source; load() })

  const icons = { SavedClips: Bookmark, SentryClips: Shield, RecentClips: Route, TeslaTrackMode: Flag }
  const filters = $derived([
    { id: 'all', label: t('clips.all') },
    { id: 'SavedClips', label: t('cat.SavedClips') },
    { id: 'SentryClips', label: t('cat.SentryClips') },
    { id: 'RecentClips', label: t('clips.trips') },
    { id: 'TeslaTrackMode', label: t('cat.TeslaTrackMode') },
  ])

  const visible = $derived.by(() => {
    if (!events) return []
    const needle = q.trim().toLowerCase()
    return events.filter((e) => (cat === 'all' || e.category === cat) &&
      (!needle || [e.meta?.city, e.meta?.street, reason(e.meta?.reason)].some((v) => v?.toLowerCase().includes(needle))))
  })
  const counts = $derived(Object.fromEntries(filters.map((f) => [f.id, events ? events.filter((e) => f.id === 'all' || e.category === f.id).length : 0])))
  const groups = $derived.by(() => {
    const out = []
    for (const e of visible) {
      const d = carDate(e.start)
      const key = d ? d.toDateString() : 'sin fecha'
      let g = out.at(-1)
      if (!g || g.key !== key) { g = { key, label: d ? dayLabel(d) : t('clips.no_date'), items: [] }; out.push(g) }
      g.items.push(e)
    }
    return out
  })

  const dur = (e) => { const a = carDate(e.start), b = carDate(e.end); return a && b ? (b - a) / 1000 : null }
  const href = (e) => `#/clip/${source}/${encodeURIComponent(e.dir)}/${encodeURIComponent(e.id)}`

  async function reread() {
    loading = true
    try { await post('/api/refresh'); await load() }
    catch (e) { toast(e.message, 'error'); loading = false }
  }
</script>

<div class="flex flex-wrap items-end gap-4">
  <div>
    <p class="eyebrow">{t('clips.eyebrow')}</p>
    <h1 class="mt-1 text-2xl font-semibold tracking-tight">{t('nav.clips')}</h1>
  </div>
  <div class="ml-auto inline-flex rounded-xl border border-[var(--border)] bg-[var(--surface)] p-1">
    {#each [{ id: 'pi', label: t('clips.on_pi'), icon: HardDrive }, { id: 'nas', label: t('clips.on_nas'), icon: Server }] as o}
      <button class="flex h-8 items-center gap-2 rounded-lg px-3 text-sm font-medium transition-colors
                     {source === o.id ? 'bg-[var(--surface-2)] text-[var(--text)] shadow-sm' : 'text-[var(--text-3)]'}"
        onclick={() => (source = o.id)}><o.icon size={15} />{o.label}</button>
    {/each}
  </div>
</div>

<div class="mt-5 flex flex-col gap-3 sm:flex-row sm:items-center">
  <div class="-mx-4 flex gap-2 overflow-x-auto px-4 pb-1 sm:mx-0 sm:px-0">
    {#each filters as f}
      <button class="chip" aria-pressed={cat === f.id} onclick={() => (cat = f.id)}>
        {#if f.id !== 'all'}<span class="h-2 w-2 rounded-full" style="background: {categories[f.id].color}"></span>{/if}
        {f.label}<span class="num text-[var(--text-3)]">{counts[f.id]}</span>
      </button>
    {/each}
  </div>
  <label class="relative sm:ml-auto sm:w-64">
    <Search size={16} class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-[var(--text-3)]" />
    <input class="input !pl-9" placeholder={t('clips.search')} bind:value={q} />
  </label>
</div>

{#if loading && !events}
  <div class="flex h-64 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
{:else if error}
  <div class="card mt-6 flex flex-col items-center gap-3 px-6 py-14 text-center">
    <Film size={28} class="text-[var(--text-3)]" />
    <p class="max-w-md text-[var(--text-2)]">{error}</p>
    {#if source === 'pi'}<button class="btn" onclick={reread}><RefreshCw size={15} />{t('clips.read_drive')}</button>{/if}
  </div>
{:else if visible.length === 0}
  <div class="card mt-6 px-6 py-14 text-center text-[var(--text-2)]">{t('clips.none')}</div>
{:else}
  {#each groups as g (g.key)}
    <h2 class="mb-3 mt-8 text-sm font-semibold text-[var(--text-2)]">{g.label}</h2>
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      {#each g.items as e (e.dir + e.id)}
        {@const Icon = icons[e.category] ?? Film}
        {@const c = categories[e.category] ?? categories.Other}
        <a href={href(e)} class="card group overflow-hidden transition-all hover:-translate-y-0.5 hover:border-[var(--border-strong)]">
          <div class="relative aspect-[16/9] overflow-hidden bg-[var(--surface-2)]">
            {#if e.thumb && !e.encrypted}
              <img src={mediaURL(source, e.thumb)} alt="" loading="lazy" class="h-full w-full object-cover transition-transform duration-500 group-hover:scale-[1.03]" />
            {:else}
              <div class="flex h-full w-full items-center justify-center"
                style="background: radial-gradient(120% 90% at 20% 0%, color-mix(in oklab, {c.color} 28%, transparent), transparent 70%)">
                {#if e.encrypted}<Lock size={30} class="text-[var(--text-3)]" />{:else}<Icon size={30} class="text-[var(--text-3)]" />{/if}
              </div>
            {/if}
            <div class="absolute inset-x-0 top-0 flex items-center gap-1.5 p-2.5">
              <span class="badge !bg-black/55 !text-white backdrop-blur"><span class="h-1.5 w-1.5 rounded-full" style="background: {c.color}"></span>{catShort(e.category)}</span>
              {#if e.encrypted}<span class="badge !bg-black/55 !text-white backdrop-blur"><Lock size={11} />{t('clips.encrypted')}</span>{/if}
              {#if e.archived && source === 'pi'}<span class="badge ml-auto !bg-black/55 !text-white backdrop-blur" title={t('clips.in_nas_title')}><CloudCheck size={12} />{t('clips.in_nas')}</span>{/if}
            </div>
            {#if dur(e)}<span class="badge num absolute bottom-2.5 right-2.5 !bg-black/55 !text-white backdrop-blur">{duration(dur(e))}</span>{/if}
          </div>
          <div class="p-4">
            <div class="flex items-baseline gap-2">
              <span class="num text-[15px] font-semibold">{time(carDate(e.start))}</span>
              <span class="truncate text-sm text-[var(--text-2)]">{reason(e.meta?.reason) ?? (e.category === 'RecentClips' ? t('clips.continuous') : '')}</span>
            </div>
            <p class="mt-1.5 flex items-center gap-1.5 truncate text-sm text-[var(--text-3)]">
              {#if e.meta?.city}<MapPin size={13} class="shrink-0" />{[e.meta.street, e.meta.city].filter(Boolean).join(', ')}
              {:else}{t('clips.cameras', { n: e.cameras.length })}{/if}
              <span class="num ml-auto shrink-0">{bytes(e.bytes)}</span>
            </p>
          </div>
        </a>
      {/each}
    </div>
  {/each}
{/if}
