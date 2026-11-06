<script>
  // Anillo de estado: el progreso del archivado y, a su alrededor, un movimiento que dice qué está
  // haciendo la Pi (conectando, leyendo, copiando al ritmo real, liberando, error…).
  //
  // Coste: todo son animaciones CSS sobre `transform` y `opacity` en capas propias, que el navegador
  // compone en la GPU sin ejecutar JavaScript ni repintar por fotograma. No hay temporizadores ni
  // sondeo: lo único que cambia es un atributo cuando llega el estado por SSE. Con
  // prefers-reduced-motion todo queda quieto.
  //
  // phase: idle | sleep | connecting | snapshot | copying | verifying | freeing | done | interrupted | error
  // beat:  segundos por latido mientras copia (más rápido = más velocidad de copia).
  // pulse: contador (ficheros copiados); cada cambio lanza una onda.
  let { value = 0, phase = 'idle', beat = 1.2, pulse = 0, size = 132, stroke = 10, children } = $props()
  const r = $derived((size - stroke) / 2)
  const c = $derived(2 * Math.PI * r)
  const f = $derived(Math.min(1, Math.max(0, value)))
  const arc = $derived(c * 0.24) // longitud de los arcos que giran
  const gid = 'rg' + Math.random().toString(36).slice(2, 8)
  const showHead = $derived(phase === 'copying' && f > 0.015 && f < 0.995)
</script>

<div class="st-ring" data-phase={phase} style="--size:{size}px; --beat:{beat}s; --stroke:{stroke}px; --f:{f}">
  <span class="halo"></span>
  {#if phase === 'copying' && pulse > 0}{#key pulse}<span class="ripple"></span>{/key}{/if}

  <svg class="base" width={size} height={size} viewBox="0 0 {size} {size}" aria-hidden="true">
    <defs>
      <linearGradient id={gid} x1="0" y1="0" x2="1" y2="1">
        <stop offset="0" stop-color="var(--arc-a)" />
        <stop offset="1" stop-color="var(--arc-b)" />
      </linearGradient>
    </defs>
    <g transform="rotate(-90 {size / 2} {size / 2})">
      <circle cx={size / 2} cy={size / 2} {r} fill="none" stroke="var(--surface-2)" stroke-width={stroke} />
      <circle cx={size / 2} cy={size / 2} {r} fill="none" stroke="url(#{gid})" stroke-width={stroke}
        stroke-linecap="round" stroke-dasharray={c} stroke-dashoffset={c * (1 - f)} class="prog" />
    </g>
  </svg>

  <!-- Arcos que giran (cada uno en su propio <svg>, para que rote como capa y no repinte) -->
  <svg class="spin a" width={size} height={size} viewBox="0 0 {size} {size}" aria-hidden="true">
    <circle cx={size / 2} cy={size / 2} {r} fill="none" stroke="url(#{gid})" stroke-width={stroke}
      stroke-linecap="round" stroke-dasharray="{arc} {c - arc}" />
  </svg>
  <svg class="spin b" width={size} height={size} viewBox="0 0 {size} {size}" aria-hidden="true">
    <circle cx={size / 2} cy={size / 2} {r} fill="none" stroke="var(--arc-b)" stroke-width={stroke * 0.55}
      stroke-linecap="round" stroke-dasharray="{arc * 0.6} {c - arc * 0.6}" opacity=".7" />
  </svg>

  {#if showHead}<span class="head"><i></i></span>{/if}

  <div class="center">{@render children?.()}</div>
</div>

<style>
  .st-ring {
    --arc-a: var(--accent);
    --arc-b: var(--accent-2);
    --tone: var(--accent);
    position: relative; flex-shrink: 0;
    width: var(--size); height: var(--size);
  }
  .st-ring[data-phase='error'] { --arc-a: var(--critical); --arc-b: var(--critical); --tone: var(--critical); }
  .st-ring[data-phase='interrupted'] { --arc-a: var(--warning); --arc-b: var(--warning); --tone: var(--warning); }

  .st-ring > svg, .halo, .ripple, .head, .center { position: absolute; }
  .st-ring > svg { inset: 0; }
  .center { inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; }

  .prog { transition: stroke-dashoffset 0.6s ease; }

  /* Halo: resplandor detrás del anillo. Es el que respira y late. */
  .halo {
    inset: -16%; border-radius: 50%; pointer-events: none;
    background: radial-gradient(circle, color-mix(in srgb, var(--tone) 55%, transparent) 0%, transparent 66%);
    opacity: 0.18; will-change: transform, opacity;
  }
  .st-ring[data-phase='idle'] .halo { animation: breathe 5s ease-in-out infinite; }
  .st-ring[data-phase='sleep'] .halo { opacity: 0.07; animation: breathe 9s ease-in-out infinite; }
  .st-ring[data-phase='connecting'] .halo,
  .st-ring[data-phase='freeing'] .halo { animation: breathe 2s ease-in-out infinite; }
  .st-ring[data-phase='snapshot'] .halo,
  .st-ring[data-phase='verifying'] .halo { animation: breathe 2.6s ease-in-out infinite; }
  .st-ring[data-phase='copying'] .halo { animation: heartbeat var(--beat) ease-in-out infinite; }
  .st-ring[data-phase='done'] .halo { opacity: 0.22; }
  .st-ring[data-phase='interrupted'] .halo { animation: blink 2.6s ease-in-out infinite; }
  .st-ring[data-phase='error'] .halo { animation: blink 1.8s ease-in-out infinite; }

  /* Arcos giratorios: solo mientras se conecta, se lee o se libera espacio. */
  .spin { opacity: 0; will-change: transform; }
  .st-ring[data-phase='connecting'] .spin.a { opacity: 1; animation: spin 1.1s linear infinite; }
  .st-ring[data-phase='snapshot'] .spin.a,
  .st-ring[data-phase='verifying'] .spin.a { opacity: 1; animation: spin 1.7s linear infinite; }
  .st-ring[data-phase='snapshot'] .spin.b,
  .st-ring[data-phase='verifying'] .spin.b { opacity: 1; animation: spin 2.6s linear infinite reverse; }
  .st-ring[data-phase='freeing'] .spin.b { opacity: 1; animation: spin 1.4s linear infinite reverse; }

  /* Onda por cada fichero copiado. */
  .ripple {
    inset: 0; border-radius: 50%; pointer-events: none; will-change: transform, opacity;
    border: 2px solid color-mix(in srgb, var(--tone) 70%, transparent);
    animation: ripple 1s ease-out 1 forwards;
  }

  /* «Cabeza» luminosa en el extremo del progreso. */
  .head {
    inset: 0; pointer-events: none; will-change: transform;
    transform: rotate(calc(var(--f) * 360deg)); transition: transform 0.6s ease;
  }
  .head i {
    position: absolute; left: 50%; top: calc(var(--stroke) / 2);
    width: calc(var(--stroke) * 1.15); height: calc(var(--stroke) * 1.15);
    margin: calc(var(--stroke) * -0.575) 0 0 calc(var(--stroke) * -0.575);
    border-radius: 50%; background: #fff;
    box-shadow: 0 0 10px 2px color-mix(in srgb, var(--arc-b) 85%, transparent);
  }

  /* Al terminar: un único «pop». */
  .st-ring[data-phase='done'] .base { animation: pop 0.55s ease-out 1; }

  @keyframes spin { to { transform: rotate(360deg); } }
  @keyframes breathe {
    0%, 100% { opacity: 0.14; transform: scale(0.96); }
    50% { opacity: 0.34; transform: scale(1.04); }
  }
  /* «lub-dub»: dos golpes y reposo. */
  @keyframes heartbeat {
    0%   { opacity: 0.22; transform: scale(0.97); }
    12%  { opacity: 0.6;  transform: scale(1.07); }
    26%  { opacity: 0.3;  transform: scale(1);    }
    40%  { opacity: 0.48; transform: scale(1.045); }
    62%, 100% { opacity: 0.22; transform: scale(0.97); }
  }
  @keyframes blink { 0%, 100% { opacity: 0.1; } 50% { opacity: 0.42; } }
  @keyframes ripple { from { opacity: 0.55; transform: scale(1); } to { opacity: 0; transform: scale(1.28); } }
  @keyframes pop { 0% { transform: scale(1); } 45% { transform: scale(1.06); } 100% { transform: scale(1); } }

  @media (prefers-reduced-motion: reduce) {
    .halo, .spin, .ripple, .base { animation: none !important; }
    .halo { opacity: 0.2 !important; }
    .spin { opacity: 0 !important; }
    .ripple { display: none; }
    .head { transition: none; }
  }
</style>
