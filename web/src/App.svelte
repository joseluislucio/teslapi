<script>
  import { onMount } from 'svelte'
  import { Gauge, Film, CloudUpload, Settings2, Cpu, FlaskConical, CircleAlert, CircleCheck, Info, LoaderCircle, LogOut } from '@lucide/svelte'
  import { live, connect, toasts, toast } from './lib/status.svelte.js'
  import { get, post, auth } from './lib/api.js'
  import { t, setLocale, pick } from './lib/i18n.svelte.js'
  import LangPicker from './components/LangPicker.svelte'
  import Login from './pages/Login.svelte'
  import Wizard from './pages/Wizard.svelte'
  import ModePill from './components/ModePill.svelte'
  import Home from './pages/Home.svelte'
  import Clips from './pages/Clips.svelte'
  import Viewer from './pages/Viewer.svelte'
  import Archive from './pages/Archive.svelte'
  import Settings from './pages/Settings.svelte'
  import System from './pages/System.svelte'

  let hash = $state(location.hash)
  async function loadAuth() {
    try {
      const a = await get('/api/auth')
      Object.assign(auth, { required: a.required, authenticated: a.authenticated, setupDone: a.setup_done, language: a.language })
      setLocale(pick(a.language))
    } catch { setLocale(pick('')) }
    auth.ready = true
  }
  // La conexión en vivo empieza cuando ya hay sesión.
  let connected = false
  $effect(() => {
    if (auth.ready && auth.authenticated && !connected) { connected = true; connect() }
  })
  async function logout() { await post('/api/auth/logout'); auth.authenticated = false; connected = false }
  onMount(() => {
    loadAuth()
    const on = () => { hash = location.hash; window.scrollTo({ top: 0 }) }
    addEventListener('hashchange', on)
    return () => removeEventListener('hashchange', on)
  })

  const route = $derived.by(() => {
    const parts = hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent)
    return { name: parts[0] || '', params: parts.slice(1) }
  })

  const nav = [
    { href: '#/', name: '', key: 'nav.home', icon: Gauge },
    { href: '#/clips', name: 'clips', key: 'nav.clips', icon: Film },
    { href: '#/archivado', name: 'archivado', key: 'nav.archive', icon: CloudUpload },
    { href: '#/ajustes', name: 'ajustes', key: 'nav.settings', icon: Settings2 },
    { href: '#/sistema', name: 'sistema', key: 'nav.system', icon: Cpu },
  ]
  const active = (n) => route.name === n || (n === 'clips' && route.name === 'clip')

  const s = $derived(live.status)

  async function simulate(present) {
    try { await post('/api/dev/charger', { present }) }
    catch (e) { toast(e.message, 'error') }
  }
</script>

{#if !auth.ready}
  <div class="flex h-screen items-center justify-center text-[var(--text-3)]"><LoaderCircle class="animate-spin" /></div>
{:else if !auth.authenticated}
  <Login />
{:else if !auth.setupDone}
  <Wizard />
{:else}
<header class="sticky top-0 z-30 border-b border-[var(--border)] backdrop-blur-xl" style="background: color-mix(in oklab, var(--bg) 72%, transparent)">
  <div class="mx-auto flex h-16 max-w-6xl items-center gap-4 px-4 sm:px-6">
    <a href="#/" class="flex items-center gap-2.5 font-semibold tracking-tight">
      <img src="/logo.png" alt="" class="h-8 w-8" />
      <span class="text-[17px]">teslapi</span>
    </a>
    <nav class="ml-6 hidden items-center gap-1 md:flex">
      {#each nav as n}
        <a href={n.href}
          class="flex h-9 items-center gap-2 rounded-xl px-3 text-sm font-medium transition-colors
                 {active(n.name) ? 'bg-[var(--surface-2)] text-[var(--text)]' : 'text-[var(--text-2)] hover:text-[var(--text)]'}">
          <n.icon size={16} strokeWidth={2} />{t(n.key)}
        </a>
      {/each}
    </nav>
    <div class="ml-auto flex items-center gap-3">
      <span class="hidden sm:block"><LangPicker compact /></span>
      {#if auth.required}<button class="btn !h-9 !px-2.5" onclick={logout} aria-label={t('login.logout')} title={t('login.logout')}><LogOut size={15} /></button>{/if}
      {#if !live.connected}
        <span class="badge" style="color: var(--warning)"><span class="pulse h-1.5 w-1.5 rounded-full bg-current"></span>{t('app.reconnecting')}</span>
      {/if}
      {#if s}<ModePill status={s} />{/if}
    </div>
  </div>
</header>

<main class="mx-auto max-w-6xl px-4 pb-28 pt-6 sm:px-6 md:pb-16">
  {#if route.name === ''}
    <Home />
  {:else if route.name === 'clips'}
    <Clips />
  {:else if route.name === 'clip'}
    <Viewer source={route.params[0]} dir={route.params[1]} id={route.params[2]} />
  {:else if route.name === 'archivado'}
    <Archive />
  {:else if route.name === 'ajustes'}
    <Settings />
  {:else if route.name === 'sistema'}
    <System />
  {:else}
    <p class="text-[var(--text-2)]">{t('app.not_found')}</p>
  {/if}
</main>

<!-- Barra inferior en el móvil -->
<nav class="fixed inset-x-0 bottom-0 z-30 border-t border-[var(--border)] backdrop-blur-xl md:hidden"
  style="background: color-mix(in oklab, var(--bg) 80%, transparent); padding-bottom: env(safe-area-inset-bottom)">
  <div class="grid grid-cols-5">
    {#each nav as n}
      <a href={n.href} class="flex flex-col items-center gap-1 py-2.5 text-[11px] font-medium
             {active(n.name) ? 'text-[var(--accent)]' : 'text-[var(--text-3)]'}">
        <n.icon size={20} strokeWidth={2} />{t(n.key)}
      </a>
    {/each}
  </div>
</nav>

{#if s?.dev}
  <div class="card fixed bottom-24 left-4 z-40 flex items-center gap-3 px-3 py-2 text-xs md:bottom-4">
    <FlaskConical size={14} style="color: var(--accent)" />
    <span class="text-[var(--text-2)]">{t('dev.label')}</span>
    <button class="chip !h-7" aria-pressed={s.charger.present} onclick={() => simulate(!s.charger.present)}>
      {s.charger.present ? t('dev.leave') : t('dev.arrive')}
    </button>
  </div>
{/if}

{/if}

<div class="pointer-events-none fixed right-4 top-20 z-50 flex w-[min(380px,calc(100vw-2rem))] flex-col gap-2">
  {#each toasts.list as t (t.id)}
    <div class="card pointer-events-auto flex items-start gap-3 px-4 py-3 text-sm">
      {#if t.kind === 'error'}<CircleAlert size={18} style="color: var(--critical)" class="mt-0.5 shrink-0" />
      {:else if t.kind === 'ok'}<CircleCheck size={18} style="color: var(--good)" class="mt-0.5 shrink-0" />
      {:else}<Info size={18} style="color: var(--accent)" class="mt-0.5 shrink-0" />{/if}
      <span>{t.text}</span>
    </div>
  {/each}
</div>
