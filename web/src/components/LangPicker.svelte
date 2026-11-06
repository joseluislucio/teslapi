<script>
  import { Languages } from '@lucide/svelte'
  import { locales, i18n, setLocale, t } from '../lib/i18n.svelte.js'
  let { compact = false } = $props()
</script>

<label class="relative inline-flex items-center">
  <Languages size={15} class="pointer-events-none absolute left-3 text-[var(--text-3)]" />
  <select class="input !h-9 !w-auto !py-0 !pl-8 text-sm" aria-label={t('lang.label')} value={i18n.locale}
    onchange={(e) => setLocale(e.currentTarget.value, true)}>
    {#each locales as l}<option value={l.id}>{compact ? l.id.toUpperCase() : l.name}</option>{/each}
  </select>
</label>
