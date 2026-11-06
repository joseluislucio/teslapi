<script>
  import { Lock, LoaderCircle } from '@lucide/svelte'
  import { post, auth } from '../lib/api.js'
  import { t } from '../lib/i18n.svelte.js'
  import LangPicker from '../components/LangPicker.svelte'

  let password = $state('')
  let error = $state('')
  let busy = $state(false)
  async function submit(e) {
    e.preventDefault()
    busy = true
    error = ''
    try { await post('/api/auth/login', { password }); auth.authenticated = true }
    catch (err) { error = err.message; password = '' }
    finally { busy = false }
  }
</script>

<div class="mx-auto flex min-h-[80vh] max-w-sm flex-col justify-center px-4">
  <div class="mb-6 flex items-center justify-between">
    <span class="flex items-center gap-2.5 text-[17px] font-semibold tracking-tight"><img src="/logo.png" alt="" class="h-8 w-8" />teslapi</span>
    <LangPicker compact />
  </div>
  <form class="card p-6" onsubmit={submit}>
    <span class="flex h-11 w-11 items-center justify-center rounded-xl bg-[var(--surface-2)]"><Lock size={20} /></span>
    <h1 class="mt-4 text-xl font-semibold tracking-tight">{t('login.title')}</h1>
    <p class="mt-1 text-sm text-[var(--text-2)]">{t('login.text')}</p>
    <input class="input mt-5" type="password" autocomplete="current-password" aria-label={t('login.password')} placeholder={t('login.password')} bind:value={password} />
    {#if error}<p class="mt-2 text-sm" style="color: var(--critical)">{error}</p>{/if}
    <button class="btn btn-primary mt-4 w-full justify-center" disabled={!password || busy}>
      {#if busy}<LoaderCircle size={16} class="animate-spin" />{/if}{t('login.enter')}
    </button>
  </form>
</div>
