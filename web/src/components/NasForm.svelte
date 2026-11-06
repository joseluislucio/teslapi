<script>
  // Campos del destino SMB con "Probar conexión", que monta de verdad y escribe un fichero.
  import { Check, CircleAlert, LoaderCircle, PlugZap } from '@lucide/svelte'
  import { post } from '../lib/api.js'
  import { t } from '../lib/i18n.svelte.js'

  let { cfg, ontested } = $props()
  let state = $state({ busy: false, ok: false, error: '' })
  async function test() {
    state = { busy: true, ok: false, error: '' }
    try { await post('/api/nas/test', cfg); state = { busy: false, ok: true, error: '' }; ontested?.(true) }
    catch (e) { state = { busy: false, ok: false, error: e.message }; ontested?.(false) }
  }
  // Cualquier cambio invalida la prueba anterior.
  $effect(() => { JSON.stringify(cfg.smb); state = { busy: false, ok: false, error: '' }; ontested?.(false) })
</script>

{#snippet field(label, hint)}
  <span class="mb-1.5 block text-sm font-medium">{label}</span>
  {#if hint}<span class="mb-2 block text-xs text-[var(--text-3)]">{hint}</span>{/if}
{/snippet}

<div class="grid gap-4 sm:grid-cols-2">
  <label class="sm:col-span-2">{@render field(t('nas.share'), t('nas.share_hint'))}<input class="input num" placeholder="//servidor/recurso" bind:value={cfg.smb.share} /></label>
  <label>{@render field(t('nas.folder'))}<input class="input" bind:value={cfg.smb.subdir} /></label>
  <label>{@render field(t('nas.domain'))}<input class="input" placeholder={t('nas.optional')} bind:value={cfg.smb.domain} /></label>
  <label>{@render field(t('nas.user'))}<input class="input" autocomplete="off" bind:value={cfg.smb.user} /></label>
  <label>{@render field(t('nas.password'))}<input class="input" type="password" autocomplete="off" bind:value={cfg.smb.password} /></label>
  <label class="flex items-start gap-3 sm:col-span-2">
    <input type="checkbox" class="mt-0.5 h-4 w-4 accent-[var(--accent)]" bind:checked={cfg.smb.encrypt} />
    <span class="text-sm"><span class="font-medium">{t('nas.encrypt')}</span>
      <span class="block text-xs text-[var(--text-3)]">{t('nas.encrypt_hint')}</span></span>
  </label>
</div>
<div class="mt-4 flex flex-wrap items-center gap-3">
  <button class="btn" disabled={!cfg.smb.share || state.busy} onclick={test}>
    {#if state.busy}<LoaderCircle size={16} class="animate-spin" />{:else}<PlugZap size={16} />{/if}{t('nas.test')}
  </button>
  {#if state.ok}<span class="inline-flex items-center gap-1.5 text-sm" style="color: var(--good)"><Check size={16} />{t('nas.test_ok')}</span>{/if}
  {#if state.error}<span class="inline-flex items-start gap-1.5 text-sm" style="color: var(--critical)"><CircleAlert size={16} class="mt-0.5 shrink-0" />{state.error}</span>{/if}
</div>
