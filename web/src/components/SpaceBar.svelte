<script>
  // Barra segmentada del espacio del pendrive (parte de un todo). Separación de
  // 2 px entre segmentos, extremos redondeados, tooltip por segmento y leyenda
  // con valores, que hace de tabla (los colores de serie claros no llegan a 3:1).
  import { bytes, categories, catLabel, n0 } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'
  let { usage = [], total = 0, free = 0 } = $props()

  const order = ['SavedClips', 'SentryClips', 'RecentClips', 'TeslaTrackMode', 'Other']
  const rows = $derived.by(() => {
    const by = Object.fromEntries((usage ?? []).map((u) => [u.category, u]))
    return order.filter((c) => by[c]?.bytes > 0).map((c) => ({ ...by[c], ...categories[c], label: catLabel(c) }))
  })
  // Lo que no es ni clip ni libre (metadatos del FS, ficheros sin clasificar).
  const used = $derived(Math.max(0, total - free))
  const pct = (b) => (total > 0 ? (b / total) * 100 : 0)
  let hover = $state(null)
</script>

<div class="relative">
  <!-- El fondo de la pista es el espacio usado que no es de ningún clip (sistema de ficheros, restos). -->
  <div class="flex h-3.5 w-full gap-[2px] overflow-hidden rounded-[4px] bg-[var(--surface-2)]" role="img"
    aria-label={t('space.aria', { used: bytes(used), total: bytes(total) })}>
    {#each rows as r}
      <div class="h-full min-w-[3px] transition-opacity" style="width: {pct(r.bytes)}%; background: {r.color}; opacity: {hover && hover !== r.category ? 0.45 : 1}"
        role="presentation"
        onmouseenter={() => (hover = r.category)} onmouseleave={() => (hover = null)}></div>
    {/each}
    <div class="ml-auto h-full" style="width: {pct(free)}%; background: var(--free)"></div>
  </div>
  {#if hover}
    {@const r = rows.find((x) => x.category === hover)}
    <div class="card pointer-events-none absolute -top-14 left-0 z-10 px-3 py-2 text-xs">
      <span class="font-semibold">{r.label}</span>
      <span class="num text-[var(--text-2)]"> · {bytes(r.bytes)} · {t('space.files', { n: n0(r.files) })} · {pct(r.bytes).toFixed(1)} %</span>
    </div>
  {/if}
</div>

<ul class="mt-4 grid grid-cols-1 gap-x-8 gap-y-2.5 text-sm min-[420px]:grid-cols-2 lg:grid-cols-3">
  {#each rows as r}
    <li class="flex items-center gap-2.5">
      <span class="h-2.5 w-2.5 shrink-0 rounded-[3px]" style="background: {r.color}"></span>
      <span class="text-[var(--text-2)]">{r.label}</span>
      <span class="num ml-auto whitespace-nowrap font-medium">{bytes(r.bytes)}</span>
    </li>
  {/each}
  <li class="flex items-center gap-2.5">
    <span class="h-2.5 w-2.5 shrink-0 rounded-[3px] border border-[var(--border-strong)]" style="background: var(--free)"></span>
    <span class="text-[var(--text-2)]">{t('space.free')}</span>
    <span class="num ml-auto whitespace-nowrap font-medium">{bytes(free)}</span>
  </li>
</ul>
