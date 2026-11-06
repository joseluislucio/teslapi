<script>
  // Tiempo hasta que el coche ve el pendrive en cada arranque. Una sola serie
  // (sin leyenda: el título la nombra), línea de referencia del objetivo y tooltip por barra.
  import { n1 } from '../lib/format.js'
  import { t as tr } from '../lib/i18n.svelte.js'
  let { times = [], target = 6 } = $props()

  let W = $state(560)
  const H = 150, PAD = { l: 30, r: 8, t: 10, b: 20 }
  const max = $derived(Math.max(target * 1.3, ...times, 1))
  const y = (v) => PAD.t + (1 - v / max) * (H - PAD.t - PAD.b)
  const bw = $derived(times.length ? Math.min(28, (W - PAD.l - PAD.r) / times.length - 4) : 0)
  const x = (i) => PAD.l + i * ((W - PAD.l - PAD.r) / Math.max(times.length, 1)) + 2
  const ticks = $derived([0, Math.round(max / 2), Math.round(max)])
  let hover = $state(-1)
</script>

{#if times.length === 0}
  <p class="py-8 text-center text-sm text-[var(--text-3)]">{tr('boot.none')}</p>
{:else}
  <div class="relative" bind:clientWidth={W}>
    <svg width={W} height={H} viewBox="0 0 {W} {H}" class="block" role="img" aria-label={tr('boot.aria')}>
      {#each ticks as t}
        <line x1={PAD.l} x2={W - PAD.r} y1={y(t)} y2={y(t)} stroke="var(--border)" stroke-width="1" />
        <text x={PAD.l - 6} y={y(t) + 3.5} text-anchor="end" font-size="11" fill="var(--text-3)" class="num">{t}s</text>
      {/each}
      <line x1={PAD.l} x2={W - PAD.r} y1={y(target)} y2={y(target)} stroke="var(--text-3)" stroke-dasharray="4 4" stroke-width="1" />
      <text x={W - PAD.r} y={y(target) - 4} text-anchor="end" font-size="11" fill="var(--text-2)">{tr('boot.target', { s: target })}</text>
      {#each times as v, i}
        {@const h = Math.max(2, y(0) - y(v))}
        <!-- barra con extremo superior redondeado (4 px) anclada a la base -->
        <path d="M{x(i)},{y(0)} v{-(h - 4)} q0,-4 4,-4 h{bw - 8} q4,0 4,4 v{h - 4} z"
          fill="var(--series-1)" opacity={hover >= 0 && hover !== i ? 0.5 : 1} />
        <rect x={x(i) - 2} y={PAD.t} width={bw + 4} height={H - PAD.t - PAD.b} fill="transparent"
          role="presentation" onmouseenter={() => (hover = i)} onmouseleave={() => (hover = -1)} />
      {/each}
    </svg>
    {#if hover >= 0}
      <div class="card pointer-events-none absolute top-0 px-3 py-1.5 text-xs" style="left: {(x(hover) / W) * 100}%">
        <span class="num font-semibold">{n1(times[hover])} s</span>
        <span class="text-[var(--text-2)]"> · {times.length - hover === 1 ? tr('boot.latest') : `−${times.length - hover - 1}`}</span>
      </div>
    {/if}
  </div>
{/if}
