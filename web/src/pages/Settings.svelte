<script>
  import { onMount } from 'svelte'
  import { Zap, Wifi, Server, CloudUpload, SlidersHorizontal, LoaderCircle, Save, Languages, Lock, Wand2, House, Bluetooth, Plus, Trash2 } from '@lucide/svelte'
  import { get, post, put, auth } from '../lib/api.js'
  import { toast } from '../lib/status.svelte.js'
  import { t, i18n, locales, setLocale } from '../lib/i18n.svelte.js'
  import DevicePicker from '../components/DevicePicker.svelte'
  import NasForm from '../components/NasForm.svelte'

  let cfg = $state(null)
  let orig = ''
  let saving = $state(false)
  onMount(async () => {
    try { cfg = await get('/api/config'); cfg.charger.extra ??= []; cfg.wifi.extra ??= []; orig = JSON.stringify(cfg) }
    catch (e) { toast(e.message, 'error') }
  })
  const dirty = $derived(cfg && JSON.stringify(cfg) !== orig)

  async function save() {
    saving = true
    try {
      cfg = await put('/api/config', cfg); orig = JSON.stringify(cfg); toast(t('settings.saved'), 'ok')
      if (cfg.language) setLocale(cfg.language)
    }
    catch (e) { toast(e.message, 'error') }
    finally { saving = false }
  }

  function choose(d) {
    cfg.charger.address = d.address
    cfg.charger.name = d.name || ''
    toast(t('settings.device_chosen', { name: d.name || d.address }))
  }

  function addExtra(d) {
    const all = [cfg.charger.address, ...cfg.charger.extra.map((e) => e.address)].map((a) => (a || '').toLowerCase())
    if (all.includes(d.address.toLowerCase())) return
    cfg.charger.extra.push({ address: d.address, name: d.name || '' })
    toast(t('settings.device_chosen', { name: d.name || d.address }))
  }

  // Contraseña del portal
  let pw = $state({ current: '', next: '', busy: false })
  async function savePassword() {
    pw.busy = true
    try {
      await put('/api/auth/password', { current: pw.current, new: pw.next })
      pw = { current: '', next: '', busy: false }
      toast(t('settings.pw_saved'), 'ok')
    } catch (e) { toast(e.message, 'error') }
    finally { pw.busy = false }
  }
  async function rerunWizard() {
    try { await post('/api/setup/restart'); auth.setupDone = false; location.hash = '#/' }
    catch (e) { toast(e.message, 'error') }
  }
</script>

{#snippet field(label, hint)}
  <span class="mb-1.5 block text-sm font-medium">{label}</span>
  {#if hint}<span class="mb-2 block text-xs text-[var(--text-3)]">{hint}</span>{/if}
{/snippet}

{#snippet header(Icon, title, text)}
  <div class="mb-5 flex items-start gap-3">
    <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-[var(--surface-2)]"><Icon size={19} /></span>
    <div><h2 class="font-semibold">{title}</h2><p class="text-sm text-[var(--text-2)]">{text}</p></div>
  </div>
{/snippet}

<div class="flex flex-wrap items-end gap-4">
  <div>
    <p class="eyebrow">{t('settings.eyebrow')}</p>
    <h1 class="mt-1 text-2xl font-semibold tracking-tight">{t('nav.settings')}</h1>
  </div>
  <button class="btn ml-auto" onclick={rerunWizard}><Wand2 size={16} />{t('settings.rerun')}</button>
</div>

{#if !cfg}
  <div class="flex h-64 items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
{:else}
  <div class="mt-5 grid gap-4 lg:grid-cols-2">
    <section class="card p-6 lg:row-span-2">
      {@render header(House, t('settings.home_title'), t('settings.home_text'))}
      <div class="grid grid-cols-2 gap-2">
        {#each [{ id: 'ble', icon: Bluetooth }, { id: 'wifi', icon: Wifi }] as o}
          <button class="chip !h-auto min-w-0 flex-col !items-start !justify-start gap-1 !rounded-2xl !p-3 text-left !whitespace-normal" aria-pressed={cfg.trigger === o.id} onclick={() => (cfg.trigger = o.id)}>
            <span class="flex items-center gap-2 font-medium"><o.icon size={16} />{t(`trigger.${o.id}`)}</span>
            <span class="text-xs font-normal text-[var(--text-3)]">{t(`trigger.${o.id}_text`)}</span>
          </button>
        {/each}
      </div>

      {#if cfg.trigger === 'ble'}
        <div class="mt-5 grid gap-4 sm:grid-cols-2">
          <label>{@render field(t('settings.ble_address'), t('settings.ble_address_hint'))}<input class="input num" placeholder="AA:BB:CC:DD:EE:FF" bind:value={cfg.charger.address} /></label>
          <label>{@render field(t('settings.ble_name'), t('settings.ble_name_hint'))}<input class="input" bind:value={cfg.charger.name} /></label>
        </div>
        <label class="mt-4 block">
          {@render field(t('settings.rssi', { n: cfg.charger.rssi_min }), t('settings.rssi_hint'))}
          <input type="range" min="-100" max="-40" class="w-full accent-[var(--accent)]" bind:value={cfg.charger.rssi_min} />
        </label>
        <div class="mt-6">
          {@render field(t('settings.extra_devices'), t('settings.extra_devices_hint'))}
          {#each cfg.charger.extra as e, i}
            <div class="mb-2 grid grid-cols-[1fr_1fr_auto] items-center gap-2">
              <input class="input num" placeholder="AA:BB:CC:DD:EE:FF" aria-label={t('settings.ble_address')} bind:value={e.address} />
              <input class="input" placeholder={t('settings.ble_name')} aria-label={t('settings.ble_name')} bind:value={e.name} />
              <button class="btn !h-10 !w-10 !p-0" title={t('settings.remove')} aria-label={t('settings.remove')} onclick={() => cfg.charger.extra.splice(i, 1)}><Trash2 size={16} /></button>
            </div>
          {/each}
          <button class="btn !h-9" onclick={() => cfg.charger.extra.push({ address: '', name: '' })}><Plus size={14} />{t('settings.add_extra')}</button>
        </div>
        <div class="mt-6"><DevicePicker address={cfg.charger.address} extras={cfg.charger.extra.map((e) => e.address)} onchoose={choose} onadd={addExtra} /></div>
      {:else}
        <p class="mt-5 text-sm text-[var(--text-2)]">{t('settings.wifi_trigger_note', { ssid: cfg.wifi.ssid || '—' })}</p>
      {/if}
    </section>

    <section class="card p-6">
      {@render header(Wifi, t('settings.wifi_title'), t('settings.wifi_text'))}
      <div class="grid gap-4 sm:grid-cols-2">
        <label>{@render field(t('settings.ssid'))}<input class="input" bind:value={cfg.wifi.ssid} /></label>
        <label>{@render field(t('settings.password'))}<input class="input" type="password" autocomplete="off" bind:value={cfg.wifi.password} /></label>
      </div>
      <div class="mt-6">
        {@render field(t('settings.extra_wifi'), t('settings.extra_wifi_hint'))}
        {#each cfg.wifi.extra as n, i}
          <div class="mb-2 grid grid-cols-[1fr_1fr_auto] items-center gap-2">
            <input class="input" placeholder={t('settings.ssid')} aria-label={t('settings.ssid')} bind:value={n.ssid} />
            <input class="input" type="password" autocomplete="off" placeholder={t('settings.password')} aria-label={t('settings.password')} bind:value={n.password} />
            <button class="btn !h-10 !w-10 !p-0" title={t('settings.remove')} aria-label={t('settings.remove')} onclick={() => cfg.wifi.extra.splice(i, 1)}><Trash2 size={16} /></button>
          </div>
        {/each}
        <button class="btn !h-9" onclick={() => cfg.wifi.extra.push({ ssid: '', password: '' })}><Plus size={14} />{t('settings.add_wifi')}</button>
      </div>
    </section>

    <section class="card p-6">
      {@render header(Server, t('settings.nas_title'), t('settings.nas_text'))}
      <NasForm {cfg} />
    </section>

    <section class="card p-6">
      {@render header(CloudUpload, t('settings.archive_title'), t('settings.archive_text'))}
      <label class="flex items-center gap-3">
        <input type="checkbox" class="h-4 w-4 accent-[var(--accent)]" bind:checked={cfg.archive.recent_clips} />
        <span class="text-sm"><span class="font-medium">{t('settings.recent')}</span>
          <span class="block text-xs text-[var(--text-3)]">{t('settings.recent_hint')}</span></span>
      </label>
      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <label>{@render field(t('settings.free_below', { n: cfg.archive.free_space_pct }))}
          <input type="range" min="5" max="50" class="w-full accent-[var(--accent)]" bind:value={cfg.archive.free_space_pct} /></label>
        <label>{@render field(t('settings.repeat'))}<input class="input num" type="number" min="5" bind:value={cfg.archive.repeat_min} /></label>
      </div>
    </section>

    <section class="card p-6">
      {@render header(Languages, t('settings.portal_title'), t('settings.portal_text'))}
      <label class="block">{@render field(t('settings.language'), t('settings.language_hint'))}
        <select class="input" bind:value={cfg.language}>
          <option value="">{t('settings.language_auto')}</option>
          {#each locales as l}<option value={l.id}>{l.name}</option>{/each}
        </select>
      </label>
      <div class="mt-5 border-t border-[var(--border)] pt-5">
        <p class="flex items-center gap-2 text-sm font-medium"><Lock size={15} />{t('settings.pw_title')}
          <span class="badge ml-auto" style="color: var(--good)">{t('settings.pw_on')}</span></p>
        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <input class="input" type="password" autocomplete="current-password" placeholder={t('settings.pw_current')} bind:value={pw.current} />
          <input class="input" type="password" autocomplete="new-password" placeholder={t('settings.pw_new')} bind:value={pw.next} />
        </div>
        <div class="mt-3 flex gap-2">
          <button class="btn" disabled={pw.busy || !pw.current || pw.next.length < 8} onclick={savePassword}>{t('settings.pw_set')}</button>
        </div>
      </div>
    </section>

    <section class="card p-6">
      {@render header(SlidersHorizontal, t('settings.advanced_title'), t('settings.advanced_text'))}
      <div class="grid grid-cols-2 gap-4">
        <label>{@render field(t('settings.scan_on'))}<input class="input num" type="number" min="1" bind:value={cfg.charger.scan_on_sec} /></label>
        <label>{@render field(t('settings.scan_every'))}<input class="input num" type="number" min="1" bind:value={cfg.charger.scan_period_sec} /></label>
        <label>{@render field(t('settings.absent_after'))}<input class="input num" type="number" min="30" bind:value={cfg.charger.absent_after_sec} /></label>
        <label>{@render field(t('settings.seen_windows'))}<input class="input num" type="number" min="1" max="3" bind:value={cfg.charger.seen_windows} /></label>
      </div>
    </section>
  </div>

  <div class="pointer-events-none sticky bottom-20 z-20 mt-6 flex justify-end md:bottom-6">
    <button class="btn btn-primary pointer-events-auto shadow-lg" disabled={!dirty || saving} onclick={save}>
      {#if saving}<LoaderCircle size={16} class="animate-spin" />{:else}<Save size={16} />{/if}
      {dirty ? t('settings.save') : t('settings.saved_state')}
    </button>
  </div>
{/if}
