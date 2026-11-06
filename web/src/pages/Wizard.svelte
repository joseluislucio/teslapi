<script>
  // Asistente de primer arranque. Con la Pi sin Wi-Fi de casa, el móvil entra por la red
  // propia de la Pi (TeslaPi-XXXX), elige la Wi-Fi de casa y el resto se configura ya
  // desde la red de casa en teslapi.local.
  import { onMount, onDestroy } from 'svelte'
  import { Wifi, WifiOff, Lock, LoaderCircle, Check, ChevronRight, ChevronLeft, CircleAlert, Bluetooth, House, Server, ShieldCheck, Rocket, Smartphone } from '@lucide/svelte'
  import { get, post, put, auth } from '../lib/api.js'
  import { tm } from '../lib/format.js'
  import { toast } from '../lib/status.svelte.js'
  import { t, i18n, locales, setLocale } from '../lib/i18n.svelte.js'
  import LangPicker from '../components/LangPicker.svelte'
  import DevicePicker from '../components/DevicePicker.svelte'
  import NasForm from '../components/NasForm.svelte'
  import Signal from '../components/Signal.svelte'

  const steps = ['lang', 'wifi', 'home', 'nas', 'finish']
  let step = $state('lang')
  let setup = $state(null)
  let offline = $state(false)
  let cfg = $state(null)
  let busy = $state(false)
  let nasOk = $state(false)
  let chosen = $state('')
  let wifiPw = $state('')
  let manual = $state(false)
  let password = $state('')
  let timer

  async function poll() {
    try {
      const s = await get('/api/setup')
      offline = false
      setup = s
      if (s.step === 'lan' && ['lang', 'wifi', 'join'].includes(step)) step = 'home'
      else if (s.step === 'wifi' && step === 'join') step = 'wifi'
      else if (s.step === 'joining' && step !== 'join') step = 'join'
      else if (s.step === 'done') finishLocal()
    } catch { offline = true }
  }
  onMount(async () => {
    await poll()
    if (setup?.step === 'lan') step = 'home'
    timer = setInterval(poll, 2000)
  })
  onDestroy(() => clearInterval(timer))

  $effect(() => {
    if (step === 'home' && !cfg) get('/api/config').then((c) => (cfg = c)).catch((e) => toast(e.message, 'error'))
  })

  const idx = $derived(steps.indexOf(step === 'join' ? 'wifi' : step))

  async function sendWiFi() {
    busy = true
    try {
      await post('/api/setup/wifi', { ssid: chosen, password: wifiPw, language: i18n.locale })
      step = 'join'
    } catch (e) { toast(e.message, 'error') }
    finally { busy = false }
  }

  async function saveHome() {
    busy = true
    try { cfg = await put('/api/config', cfg); step = 'nas' }
    catch (e) { toast(e.message, 'error') }
    finally { busy = false }
  }
  async function saveNas() {
    busy = true
    try { cfg = await put('/api/config', cfg); step = 'finish' }
    catch (e) { toast(e.message, 'error') }
    finally { busy = false }
  }
  async function finish() {
    busy = true
    try {
      await post('/api/setup/finish', { language: i18n.locale, password })
      auth.required = true
      finishLocal()
    } catch (e) { toast(e.message, 'error') }
    finally { busy = false }
  }
  function finishLocal() { clearInterval(timer); auth.setupDone = true; location.hash = '#/' }

  const homeReady = $derived(cfg && (cfg.trigger === 'wifi' || cfg.charger.address || cfg.charger.name))
  function choose(d) { cfg.charger.address = d.address; cfg.charger.name = d.name || '' }
  const icons = { lang: Smartphone, wifi: Wifi, home: House, nas: Server, finish: ShieldCheck }
</script>

<div class="mx-auto flex min-h-screen max-w-xl flex-col px-4 py-6 sm:py-10">
  <div class="mb-6 flex items-center justify-between">
    <span class="flex items-center gap-2.5 text-[17px] font-semibold tracking-tight"><img src="/logo.png" alt="" class="h-8 w-8" />teslapi</span>
    <LangPicker compact />
  </div>

  {#if step !== 'join'}
    <ol class="mb-5 flex items-center gap-2" aria-label={t('wizard.progress')}>
      {#each steps as s, i}
        <li class="h-1.5 flex-1 rounded-full" style="background: {i <= idx ? 'var(--accent)' : 'var(--surface-2)'}" aria-current={i === idx ? 'step' : undefined}></li>
      {/each}
    </ol>
  {/if}

  <section class="card p-6 sm:p-8">
    {#if step === 'lang'}
      <h1 class="text-2xl font-semibold tracking-tight">{t('wizard.welcome')}</h1>
      <p class="mt-2 text-[var(--text-2)]">{t('wizard.welcome_text')}</p>
      <p class="mt-6 text-sm font-medium">{t('wizard.pick_language')}</p>
      <div class="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-3">
        {#each locales as l}
          <button class="chip !h-11 justify-center" aria-pressed={i18n.locale === l.id} onclick={() => setLocale(l.id, true)}>{l.name}</button>
        {/each}
      </div>
      <button class="btn btn-primary mt-8 w-full justify-center" onclick={() => (step = setup?.step === 'lan' ? 'home' : 'wifi')}>{t('wizard.start')}<ChevronRight size={16} /></button>

    {:else if step === 'wifi'}
      <h1 class="text-2xl font-semibold tracking-tight">{t('wizard.wifi_title')}</h1>
      <p class="mt-2 text-[var(--text-2)]">{t('wizard.wifi_text')}</p>
      {#if setup?.error}
        <div class="mt-4 flex items-start gap-2 rounded-xl border p-3 text-sm" style="border-color: color-mix(in oklab, var(--critical) 40%, transparent)">
          <CircleAlert size={16} class="mt-0.5 shrink-0" style="color: var(--critical)" /><span>{tm(setup.error)}</span>
        </div>
      {/if}
      {#if !manual && setup?.networks?.length}
        <ul class="mt-5 max-h-72 divide-y divide-[var(--border)] overflow-y-auto rounded-2xl border border-[var(--border)]">
          {#each setup.networks as n (n.ssid)}
            <li>
              <button class="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-[var(--surface-2)]" aria-pressed={chosen === n.ssid}
                onclick={() => { chosen = n.ssid; if (!n.secure) wifiPw = '' }}>
                <Signal dbm={-95 + n.strength * 0.6} />
                <span class="min-w-0 flex-1 truncate font-medium">{n.ssid}</span>
                {#if n.secure}<Lock size={14} class="text-[var(--text-3)]" />{/if}
                {#if chosen === n.ssid}<Check size={16} style="color: var(--accent)" />{/if}
              </button>
            </li>
          {/each}
        </ul>
        <button class="mt-2 text-sm text-[var(--accent)]" onclick={() => { manual = true; chosen = '' }}>{t('wizard.wifi_manual')}</button>
      {:else}
        <label class="mt-5 block"><span class="mb-1.5 block text-sm font-medium">{t('settings.ssid')}</span>
          <input class="input" autocapitalize="off" bind:value={chosen} /></label>
        {#if setup?.networks?.length}<button class="mt-2 text-sm text-[var(--accent)]" onclick={() => (manual = false)}>{t('wizard.wifi_list')}</button>{/if}
      {/if}
      {#if chosen}
        <label class="mt-5 block"><span class="mb-1.5 block text-sm font-medium">{t('settings.password')}</span>
          <input class="input" type="password" autocomplete="off" bind:value={wifiPw} />
          <span class="mt-1.5 block text-xs text-[var(--text-3)]">{t('wizard.wifi_2g')}</span></label>
      {/if}
      <div class="mt-8 flex gap-2">
        <button class="btn" onclick={() => (step = 'lang')}><ChevronLeft size={16} />{t('action.back')}</button>
        <button class="btn btn-primary flex-1 justify-center" disabled={!chosen || busy || (wifiPw && wifiPw.length < 8)} onclick={sendWiFi}>
          {#if busy}<LoaderCircle size={16} class="animate-spin" />{/if}{t('wizard.connect')}<ChevronRight size={16} />
        </button>
      </div>

    {:else if step === 'join'}
      <div class="flex flex-col items-center py-4 text-center">
        <LoaderCircle size={34} class="animate-spin" style="color: var(--accent)" />
        <h1 class="mt-5 text-2xl font-semibold tracking-tight">{t('wizard.joining_title', { ssid: setup?.wifi_ssid || chosen })}</h1>
        <p class="mt-2 max-w-sm text-[var(--text-2)]">{t('wizard.joining_text')}</p>
        <ol class="mt-6 w-full max-w-sm space-y-3 text-left text-sm">
          <li class="flex gap-3"><span class="badge num !h-6 !w-6 justify-center">1</span>{t('wizard.joining_1')}</li>
          <li class="flex gap-3"><span class="badge num !h-6 !w-6 justify-center">2</span><span>{t('wizard.joining_2')} <b class="num">http://teslapi.local</b></span></li>
        </ol>
        {#if offline}<p class="mt-5 inline-flex items-center gap-2 text-sm text-[var(--text-3)]"><WifiOff size={14} />{t('wizard.joining_offline')}</p>{/if}
      </div>

    {:else if step === 'home'}
      <h1 class="text-2xl font-semibold tracking-tight">{t('wizard.home_title')}</h1>
      <p class="mt-2 text-[var(--text-2)]">{t('wizard.home_text')}</p>
      {#if cfg}
        <div class="mt-5 grid gap-2">
          {#each [{ id: 'ble', icon: Bluetooth }, { id: 'wifi', icon: Wifi }] as o}
            <button class="chip !h-auto w-full min-w-0 !items-start !justify-start gap-3 !rounded-2xl !p-4 text-left !whitespace-normal" aria-pressed={cfg.trigger === o.id} onclick={() => (cfg.trigger = o.id)}>
              <o.icon size={20} class="mt-0.5 shrink-0" />
              <span><span class="block font-medium">{t(`trigger.${o.id}`)}</span><span class="block text-xs font-normal text-[var(--text-3)]">{t(`trigger.${o.id}_text`)}</span></span>
            </button>
          {/each}
        </div>
        {#if cfg.trigger === 'ble'}
          <p class="mt-5 text-sm text-[var(--text-2)]">{t('wizard.home_ble_hint')}</p>
          <div class="mt-3"><DevicePicker address={cfg.charger.address} onchoose={choose} /></div>
        {/if}
        <div class="mt-8 flex gap-2">
          <button class="btn btn-primary flex-1 justify-center" disabled={!homeReady || busy} onclick={saveHome}>
            {#if busy}<LoaderCircle size={16} class="animate-spin" />{/if}{t('action.next')}<ChevronRight size={16} />
          </button>
        </div>
        <button class="mt-3 w-full text-center text-sm text-[var(--text-3)]" onclick={() => (step = 'nas')}>{t('wizard.later')}</button>
      {:else}
        <div class="flex h-32 items-center justify-center"><LoaderCircle class="animate-spin text-[var(--text-3)]" /></div>
      {/if}

    {:else if step === 'nas'}
      <h1 class="text-2xl font-semibold tracking-tight">{t('wizard.nas_title')}</h1>
      <p class="mt-2 text-[var(--text-2)]">{t('wizard.nas_text')}</p>
      {#if cfg}
        <div class="mt-5"><NasForm {cfg} ontested={(ok) => (nasOk = ok)} /></div>
        <div class="mt-8 flex gap-2">
          <button class="btn" onclick={() => (step = 'home')}><ChevronLeft size={16} />{t('action.back')}</button>
          <button class="btn btn-primary flex-1 justify-center" disabled={!nasOk || busy} onclick={saveNas}>
            {#if busy}<LoaderCircle size={16} class="animate-spin" />{/if}{t('action.next')}<ChevronRight size={16} />
          </button>
        </div>
        <p class="mt-3 text-center text-xs text-[var(--text-3)]">{t('wizard.nas_must_test')}</p>
        <button class="mt-2 w-full text-center text-sm text-[var(--text-3)]" onclick={() => (step = 'finish')}>{t('wizard.later')}</button>
      {/if}

    {:else if step === 'finish'}
      <h1 class="text-2xl font-semibold tracking-tight">{t('wizard.finish_title')}</h1>
      <p class="mt-2 text-[var(--text-2)]">{t('wizard.finish_text')}</p>
      {#if auth.required}
        <p class="mt-5 flex items-center gap-2 text-sm text-[var(--text-2)]"><ShieldCheck size={16} />{t('wizard.pw_kept')}</p>
      {:else}
        <label class="mt-5 block"><span class="mb-1.5 block text-sm font-medium">{t('wizard.pw_label')}</span>
          <input class="input" type="password" autocomplete="new-password" required minlength="8" bind:value={password} />
          <span class="mt-1.5 block text-xs text-[var(--text-3)]">{t('wizard.pw_hint')}</span></label>
      {/if}
      <div class="mt-8 flex gap-2">
        <button class="btn" onclick={() => (step = 'nas')}><ChevronLeft size={16} />{t('action.back')}</button>
        <button class="btn btn-primary flex-1 justify-center" disabled={busy || (!auth.required && password.length < 8)} onclick={finish}>
          {#if busy}<LoaderCircle size={16} class="animate-spin" />{:else}<Rocket size={16} />{/if}{t('wizard.finish')}
        </button>
      </div>
    {/if}
  </section>
  <p class="mt-4 text-center text-xs text-[var(--text-3)]">{t('wizard.footer')}</p>
</div>
