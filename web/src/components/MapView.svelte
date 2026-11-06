<script>
  // El mapa pide teselas a OpenStreetMap, lo que revela dónde estaba el coche. Por eso no se
  // carga solo: lo pide quien mira, y queda recordado en ese navegador.
  import { onDestroy } from 'svelte'
  import { MapPin } from '@lucide/svelte'
  import { t } from '../lib/i18n.svelte.js'
  let { lat, lon, label = '' } = $props()
  let el = $state()
  let map
  const KEY = 'teslapi.map_ok'
  function stored() { try { return localStorage.getItem(KEY) === '1' } catch { return false } }
  let allowed = $state(stored())

  function allow() {
    try { localStorage.setItem(KEY, '1') } catch { /* navegación privada: vale para esta vez */ }
    allowed = true
  }

  $effect(() => {
    if (!allowed || !el || map) return
    let cancelled = false
    Promise.all([import('leaflet'), import('leaflet/dist/leaflet.css')]).then(([{ default: L }]) => {
      if (cancelled) return
      map = L.map(el, { zoomControl: false, attributionControl: true, scrollWheelZoom: false }).setView([lat, lon], 16)
      L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
        maxZoom: 19, attribution: '© OpenStreetMap contributors',
      }).addTo(map)
      // Marcador vectorial: los iconos PNG de Leaflet no sobreviven al empaquetado.
      const marker = L.circleMarker([lat, lon], { radius: 9, color: '#ffffff', weight: 3, fillColor: '#e5192c', fillOpacity: 1 }).addTo(map)
      // Leaflet trata un texto como HTML, y este viene de event.json: se pasa como nodo.
      if (label) {
        const tip = document.createElement('span')
        tip.textContent = label
        marker.bindTooltip(tip)
      }
      L.control.zoom({ position: 'bottomright' }).addTo(map)
    })
    return () => { cancelled = true }
  })
  onDestroy(() => map?.remove())
</script>

{#if allowed}
  <div bind:this={el} class="h-56 w-full overflow-hidden rounded-2xl"></div>
{:else}
  <div class="flex h-56 w-full flex-col items-center justify-center gap-3 rounded-2xl bg-[var(--surface-2)] p-5 text-center">
    <MapPin size={22} class="text-[var(--text-3)]" />
    <p class="max-w-xs text-xs text-[var(--text-2)]">{t('map.notice')}</p>
    <button class="btn !h-9" onclick={allow}>{t('map.show')}</button>
  </div>
{/if}
