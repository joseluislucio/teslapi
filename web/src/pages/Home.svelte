<script>
  import { Usb, Zap, Wifi, WifiOff, Thermometer, CloudUpload, RefreshCw, CircleCheck, CircleAlert, CirclePause, LoaderCircle, TriangleAlert, ArrowRight, HardDrive, Eraser } from '@lucide/svelte'
  import { live, toast } from '../lib/status.svelte.js'
  import { post } from '../lib/api.js'
  import { bytes, speed, duration, ago, n0, n1, tm } from '../lib/format.js'
  import { t } from '../lib/i18n.svelte.js'
  import Ring from '../components/Ring.svelte'
  import SpaceBar from '../components/SpaceBar.svelte'
  import Signal from '../components/Signal.svelte'

  const s = $derived(live.status)
  const a = $derived(s?.archive)
  const busy = $derived(['connecting', 'snapshot', 'copying', 'verifying', 'freeing'].includes(a?.phase))
  // El anillo late al ritmo real de la copia: de ~1,8 s por latido a 1 MB/s hasta ~0,6 s a 4 MB/s o más.
  const beat = $derived(Math.min(1.8, Math.max(0.6, 2.2 - 0.4 * ((a?.speed_bps || 0) / 1e6))))
  const ringPhase = $derived(a?.phase && a.phase !== 'idle' ? a.phase : s?.mode === 'away' ? 'sleep' : 'idle')
  const frac = $derived(a?.total_bytes > 0 ? a.done_bytes / a.total_bytes : a?.phase === 'done' ? 1 : 0)

  const headline = $derived.by(() => {
    if (!s) return { title: t('home.connecting'), text: '' }
    switch (a.phase) {
      case 'copying': return { title: t('home.copying'), text: a.current ? a.current.split('/').slice(-2).join(' / ') : t('home.preparing') }
      case 'connecting': return { title: t('home.connecting_nas'), text: tm(a.message) }
      case 'snapshot': return { title: t('home.reading'), text: t('home.reading_text') }
      case 'verifying': return { title: t('home.verifying'), text: tm(a.message) }
      case 'freeing': return { title: t('home.freeing'), text: tm(a.message) }
      case 'done': return { title: a.pending > 0 ? t('home.partial') : t('home.safe'), text: tm(a.message) }
      case 'interrupted': return { title: t('home.paused'), text: t('home.paused_text') }
      case 'error': return { title: t('home.failed'), text: tm(a.message) }
    }
    if (s.mode === 'home') return { title: t('home.at_home'), text: t('home.at_home_text') }
    return { title: t('home.ready'), text: t('home.ready_text') }
  })

  const usb = $derived.by(() => {
    const g = s?.drive?.gadget
    if (!g) return { label: '—', tone: 'var(--text-3)' }
    if (!g.attached) return { label: t('usb.detached'), tone: 'var(--warning)' }
    if (g.host_state === 'configured') return { label: t('usb.in_use'), tone: 'var(--good)' }
    return { label: t('usb.waiting'), tone: 'var(--text-2)' }
  })

  let starting = $state(false)
  async function archiveNow() {
    starting = true
    try { await post('/api/archive'); toast(t('toast.archive_started'), 'ok') }
    catch (e) { toast(e.message, 'error') }
    finally { starting = false }
  }
  let refreshing = $state(false)
  async function refresh() {
    refreshing = true
    try { await post('/api/refresh'); toast(t('toast.reread'), 'ok') }
    catch (e) { toast(e.message, 'error') }
    finally { refreshing = false }
  }
</script>

{#if !s}
  <div class="flex h-64 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
{:else}
  <!-- Estado principal -->
  <section class="card relative overflow-hidden p-6 sm:p-8">
    <div class="pointer-events-none absolute -right-24 -top-24 h-72 w-72 rounded-full opacity-40 blur-3xl"
      style="background: radial-gradient(circle, var(--accent), transparent 65%)"></div>
    <div class="relative flex flex-col gap-6 sm:flex-row sm:items-center">
      <Ring value={a.phase === 'copying' ? frac : a.phase === 'freeing' || a.phase === 'done' ? 1 : 0}
        phase={ringPhase} {beat} pulse={a.done_files}>
        {#if a.phase === 'copying'}
          <span class="num text-3xl font-semibold tracking-tight">{n0(frac * 100)}<span class="text-lg text-[var(--text-2)]">%</span></span>
          <span class="num text-xs text-[var(--text-3)]">{n0(a.done_files)} / {n0(a.total_files)}</span>
        {:else if a.phase === 'connecting'}
          <Wifi size={34} style="color: var(--accent)" />
        {:else if a.phase === 'snapshot' || a.phase === 'verifying'}
          <HardDrive size={34} style="color: var(--accent)" />
        {:else if a.phase === 'freeing'}
          <Eraser size={34} style="color: var(--accent)" />
        {:else if a.phase === 'done'}
          <CircleCheck size={40} style="color: var(--accent)" />
        {:else if a.phase === 'error'}
          <CircleAlert size={40} style="color: var(--critical)" />
        {:else if a.phase === 'interrupted'}
          <CirclePause size={40} style="color: var(--warning)" />
        {:else}
          <Usb size={38} style="color: var(--text-2)" />
        {/if}
      </Ring>

      <div class="min-w-0 flex-1">
        <p class="eyebrow">{t('home.status')}</p>
        <h1 class="mt-1 text-2xl font-semibold tracking-tight sm:text-3xl">{headline.title}</h1>
        {#if headline.text}<p class="mt-1.5 text-[15px] sm:truncate text-[var(--text-2)]" title={headline.text}>{headline.text}</p>{/if}

        {#if a.phase === 'copying'}
          <dl class="mt-5 grid grid-cols-3 gap-4 text-sm sm:max-w-md">
            <div><dt class="text-[var(--text-3)]">{t('home.speed')}</dt><dd class="num mt-0.5 font-medium">{speed(a.speed_bps)}</dd></div>
            <div><dt class="text-[var(--text-3)]">{t('home.copied')}</dt><dd class="num mt-0.5 font-medium">{bytes(a.done_bytes)} <span class="text-[var(--text-3)]">{t('home.of', { total: bytes(a.total_bytes) })}</span></dd></div>
            <div><dt class="text-[var(--text-3)]">{t('home.left')}</dt><dd class="num mt-0.5 font-medium">{duration(a.eta_sec)}</dd></div>
          </dl>
        {:else if a.last_run}
          <p class="mt-4 text-sm text-[var(--text-3)]">
            {t('home.last_run', { when: ago(a.last_run.end), files: n0(a.last_run.files), bytes: bytes(a.last_run.bytes) })}
            {#if a.last_run.freed > 0} · {t('home.freed', { bytes: bytes(a.last_run.freed) })}{/if}
          </p>
        {/if}
        {#if a.pending > 0 && !busy}
          <p class="mt-2 inline-flex items-center gap-1.5 text-sm" style="color: var(--warning)">
            <TriangleAlert size={15} />{t('home.pending', { n: n0(a.pending) })}
          </p>
        {/if}
      </div>

      <div class="flex flex-wrap gap-2 sm:flex-col [&>button]:flex-1 [&>button]:justify-center">
        <button class="btn btn-primary" disabled={!s.wifi.enabled || busy || starting} onclick={archiveNow}>
          <CloudUpload size={16} />{t('action.archive_now')}
        </button>
        <button class="btn" disabled={!s.wifi.enabled || busy || refreshing} onclick={refresh}>
          <RefreshCw size={16} class={refreshing ? 'animate-spin' : ''} />{t('action.reread')}
        </button>
      </div>
    </div>
  </section>

  <!-- Pendrive -->
  <section class="card mt-4 p-6">
    <div class="flex flex-wrap items-center gap-3">
      <span class="flex h-10 w-10 items-center justify-center rounded-xl bg-[var(--surface-2)]"><HardDrive size={20} /></span>
      <div>
        <h2 class="font-semibold">{t('home.car_drive')}</h2>
        <p class="text-sm" style="color: {usb.tone}">{usb.label}</p>
      </div>
      {#if s.drive.known}
        <div class="ml-auto text-right text-sm">
          <p class="num font-medium">{bytes(s.drive.total_bytes - s.drive.free_bytes)} <span class="text-[var(--text-3)]">{t('home.of', { total: bytes(s.drive.total_bytes) })}</span></p>
          <p class="text-xs text-[var(--text-3)]">{s.drive.volume.fs_type === 'exfat' ? 'exFAT' : 'FAT32'} · {t('home.read_ago', { when: ago(s.drive.snapshot_at) })}</p>
        </div>
      {/if}
    </div>
    <div class="mt-5">
      {#if s.drive.known}
        <SpaceBar usage={s.drive.usage} total={s.drive.total_bytes} free={s.drive.free_bytes} />
      {:else}
        <p class="text-sm text-[var(--text-3)]">{t('home.unknown_drive')}</p>
      {/if}
    </div>
  </section>

  <!-- Tarjetas pequeñas -->
  <section class="mt-4 grid gap-4 sm:grid-cols-3">
    <a href="#/ajustes" class="card group p-5 transition-colors hover:border-[var(--border-strong)]">
      <div class="flex items-center justify-between">
        <span class="eyebrow">{s.trigger === 'wifi' ? t('home.home_wifi') : t('home.home_device')}</span>
        <Zap size={18} style="color: {s.charger.present ? 'var(--accent)' : 'var(--text-3)'}" />
      </div>
      {#if !s.home_setup}
        <p class="mt-3 text-lg font-semibold">{t('home.not_configured')}</p>
        <p class="mt-1 flex items-center gap-1 text-sm text-[var(--accent)]">{t('home.choose_device')} <ArrowRight size={14} /></p>
      {:else}
        <p class="mt-3 flex items-center gap-2 text-lg font-semibold">
          {#if s.trigger === 'wifi'}{s.mode === 'home' ? t('home.connected') : t('home.not_connected')}{:else}{s.charger.present ? t('home.detected') : t('home.out_of_range')}{/if}
          {#if s.trigger !== 'wifi' && s.charger.present}<Signal dbm={s.charger.rssi} />{/if}
        </p>
        <p class="mt-1 text-sm text-[var(--text-2)]">
          {#if s.trigger === 'wifi'}{s.wifi.ssid || ''}{:else}{#if s.charger.present}<span class="num">{s.charger.rssi} dBm</span>{' · '}{/if}{t('home.seen', { when: ago(s.charger.last_seen) })}{/if}
        </p>
      {/if}
      {#if s.charger.error}<p class="mt-2 text-xs" style="color: var(--critical)">{s.charger.error}</p>{/if}
    </a>

    <div class="card p-5">
      <div class="flex items-center justify-between">
        <span class="eyebrow">Wi-Fi</span>
        {#if s.wifi.enabled}<Wifi size={18} style="color: var(--accent)" />{:else}<WifiOff size={18} style="color: var(--text-3)" />{/if}
      </div>
      {#if s.wifi.ap}
        <p class="mt-3 text-lg font-semibold">{t('home.setup_network')}</p>
        <p class="num mt-1 text-sm text-[var(--text-2)]">{s.wifi.ssid}</p>
      {:else if s.wifi.connected}
        <p class="mt-3 flex items-center gap-2 text-lg font-semibold">{s.wifi.ssid}<Signal dbm={s.wifi.signal} /></p>
        <p class="num mt-1 text-sm text-[var(--text-2)]">{s.wifi.ip}{#if s.nas}{' · ' + t('home.nas_mounted')}{/if}</p>
      {:else}
        <p class="mt-3 text-lg font-semibold">{s.wifi.enabled ? t('home.wifi_connecting') : t('home.wifi_off')}</p>
        <p class="mt-1 text-sm text-[var(--text-2)]">{s.wifi.enabled ? '' : t('home.wifi_auto')}</p>
      {/if}
      {#if s.wifi_error}<p class="mt-2 text-xs" style="color: var(--critical)">{tm(s.wifi_error)}</p>{/if}
    </div>

    <a href="#/sistema" class="card p-5 transition-colors hover:border-[var(--border-strong)]">
      <div class="flex items-center justify-between">
        <span class="eyebrow">Raspberry Pi</span>
        <Thermometer size={18} style="color: {s.system.temp_c > 75 ? 'var(--serious)' : 'var(--text-3)'}" />
      </div>
      <p class="num mt-3 text-lg font-semibold">{n1(s.system.temp_c)} °C</p>
      <p class="num mt-1 text-sm text-[var(--text-2)]">
        {t('home.mem_free', { mb: n0(s.system.mem_avail_mb) })}
        {#if s.system.boot_times?.length} · {t('home.boot', { s: n1(s.system.boot_times.at(-1)) })}{/if}
      </p>
      {#if s.system.throttled || s.system.throttled_since_boot}
        <p class="mt-2 flex items-center gap-1.5 text-xs" style="color: var(--serious)"><TriangleAlert size={13} />{t('system.throttled_boot')}</p>
      {/if}
      {#if s.system.undervoltage || s.system.uv_since_boot}
        <p class="mt-2 flex items-center gap-1.5 text-xs" style="color: var(--critical)"><TriangleAlert size={13} />{s.system.undervoltage ? t('home.undervoltage_now') : t('home.undervoltage_boot')}</p>
      {/if}
    </a>
  </section>
{/if}
