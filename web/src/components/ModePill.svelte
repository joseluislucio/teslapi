<script>
  import { House, Car, LifeBuoy } from '@lucide/svelte'
  import { t } from '../lib/i18n.svelte.js'
  let { status } = $props()
  const m = $derived({
    home: { label: t('mode.home'), icon: House, color: 'var(--accent)' },
    away: { label: t('mode.away'), icon: Car, color: 'var(--text-2)' },
    rescue: { label: t('mode.rescue'), icon: LifeBuoy, color: 'var(--warning)' },
    setup: { label: t('mode.setup'), icon: LifeBuoy, color: 'var(--warning)' },
  }[status.mode] ?? { label: status.mode, icon: Car, color: 'var(--text-2)' })
</script>

<span class="inline-flex h-9 items-center gap-2 rounded-full border border-[var(--border)] bg-[var(--surface)] px-3.5 text-sm font-medium" style="color: {m.color}">
  <m.icon size={15} strokeWidth={2.2} />
  <span class="text-[var(--text)]">{m.label}</span>
</span>
