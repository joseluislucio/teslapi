<script>
  // Galería de desarrollo del anillo de estado. `?still` quita el contador que lanza ondas, para medir solo CSS.
  import { onMount } from 'svelte'
  import { Usb, Wifi, HardDrive, Eraser, CircleCheck, CircleAlert, CirclePause, Moon } from '@lucide/svelte'
  import Ring from '../components/Ring.svelte'

  const still = new URLSearchParams(location.search).has('still')
  let pulse = $state(1)
  onMount(() => {
    if (still) return
    const id = setInterval(() => (pulse += 1), 1500)
    return () => clearInterval(id)
  })
  const items = [
    { k: 'idle', label: 'Reposo (en casa)', v: 0, icon: Usb },
    { k: 'sleep', label: 'Lejos de casa', v: 0, icon: Moon },
    { k: 'connecting', label: 'Conectando al NAS', v: 0, icon: Wifi },
    { k: 'snapshot', label: 'Leyendo el pendrive', v: 0, icon: HardDrive },
    { k: 'copying', label: 'Copiando · 1 MB/s', v: 0.38, beat: 1.8, pct: 38 },
    { k: 'copying', label: 'Copiando · 3 MB/s', v: 0.64, beat: 1.0, pct: 64 },
    { k: 'copying', label: 'Copiando · 6 MB/s', v: 0.87, beat: 0.6, pct: 87 },
    { k: 'freeing', label: 'Liberando espacio', v: 1, icon: Eraser },
    { k: 'done', label: 'Terminado', v: 1, icon: CircleCheck },
    { k: 'interrupted', label: 'Interrumpido', v: 0.4, icon: CirclePause },
    { k: 'error', label: 'Error', v: 0, icon: CircleAlert },
  ]
</script>

<main class="mx-auto max-w-5xl p-8">
  <h1 class="mb-6 text-xl font-semibold">Anillo de estado</h1>
  <div class="grid grid-cols-2 gap-x-6 gap-y-10 sm:grid-cols-3 lg:grid-cols-4">
    {#each items as it}
      <div class="flex flex-col items-center gap-4">
        <Ring value={it.v} phase={it.k} beat={it.beat ?? 1.2} pulse={it.k === 'copying' ? pulse : 0}>
          {#if it.pct !== undefined}
            <span class="num text-3xl font-semibold tracking-tight">{it.pct}<span class="text-lg text-[var(--text-2)]">%</span></span>
          {:else}
            <it.icon size={36} style="color: {it.k === 'error' ? 'var(--critical)' : it.k === 'interrupted' ? 'var(--warning)' : it.k === 'idle' || it.k === 'sleep' ? 'var(--text-2)' : 'var(--accent)'}" />
          {/if}
        </Ring>
        <p class="text-sm text-[var(--text-2)]">{it.label}</p>
      </div>
    {/each}
  </div>
</main>
