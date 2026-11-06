import { t, tag, i18n } from './i18n.svelte.js'

// Formateadores por idioma (se crean una vez).
const cache = new Map()
function fmt(kind, opts) {
  const key = `${kind}|${tag()}|${JSON.stringify(opts)}`
  if (!cache.has(key)) cache.set(key, kind === 'num' ? new Intl.NumberFormat(tag(), opts) : kind === 'rel' ? new Intl.RelativeTimeFormat(tag(), opts) : new Intl.DateTimeFormat(tag(), opts))
  return cache.get(key)
}
export const n0 = (v) => fmt('num', { maximumFractionDigits: 0 }).format(v)
export const n1 = (v) => fmt('num', { maximumFractionDigits: 1 }).format(v)

export function bytes(b) {
  if (b == null) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = b
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${(v >= 100 || i === 0 ? n0 : n1)(v)} ${u[i]}`
}

export function speed(bps) {
  return bps > 0 ? `${bytes(bps)}/s` : '—'
}

export function duration(sec) {
  if (sec == null || !isFinite(sec)) return '—'
  sec = Math.round(sec)
  if (sec < 60) return `${sec} s`
  const m = Math.floor(sec / 60)
  if (m < 60) return `${m} min ${String(sec % 60).padStart(2, '0')} s`
  const h = Math.floor(m / 60)
  return `${h} h ${String(m % 60).padStart(2, '0')} min`
}

// Un mensaje del daemon ({code, args, detail}) en el idioma de quien mira.
// Los tamaños llegan en bytes y se formatean aquí.
export function tm(m) {
  if (!m) return ''
  if (typeof m === 'string') return m
  if (!m.code) return m.detail || ''
  const args = { ...m.args }
  if (args.bytes != null) args.bytes = bytes(args.bytes)
  for (const k of ['files', 'pending']) if (args[k] != null) args[k] = n0(args[k])
  const key = `msg.${m.code}`
  let s = t(key, args)
  if (s === key) s = m.code // código desconocido: mejor eso que nada
  return m.detail ? `${s} (${m.detail})` : s
}

// Las horas del coche vienen como "2025-03-29_14-26-22", sin zona: se muestran tal cual.
export function carDate(stamp) {
  if (!stamp) return null
  const m = stamp.match(/^(\d{4})-(\d{2})-(\d{2})[_T](\d{2})[-:](\d{2})[-:](\d{2})/)
  if (!m) return null
  return new Date(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6])
}

const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1)

export function dayLabel(d) {
  const today = new Date()
  const diff = Math.round((new Date(today.toDateString()) - new Date(d.toDateString())) / 86400000)
  if (diff === 0 || diff === 1) return cap(fmt('rel', { numeric: 'auto' }).format(-diff, 'day'))
  const o = { weekday: 'long', day: 'numeric', month: 'long' }
  if (d.getFullYear() !== today.getFullYear()) o.year = 'numeric'
  return cap(fmt('date', o).format(d))
}
export const time = (d) => (d ? fmt('date', { hour: '2-digit', minute: '2-digit' }).format(d) : '—')
export const shortDate = (d) => (d ? fmt('date', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }).format(d) : '—')
export const clockTime = (d) => fmt('date', { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(d)

export function ago(iso) {
  if (!iso || iso.startsWith('0001')) return t('ago.never')
  const s = (Date.now() - new Date(iso).getTime()) / 1000
  const rtf = fmt('rel', { numeric: 'auto' })
  if (s < 10) return rtf.format(0, 'second')
  if (s < 60) return rtf.format(-Math.round(s), 'second')
  if (s < 3600) return rtf.format(-Math.round(s / 60), 'minute')
  if (s < 86400) return rtf.format(-Math.round(s / 3600), 'hour')
  return rtf.format(-Math.round(s / 86400), 'day')
}

// Solo colores; los nombres salen de las traducciones (cat.<id>, cat.<id>.short).
export const categories = {
  SavedClips: { color: 'var(--series-1)' },
  SentryClips: { color: 'var(--series-2)' },
  RecentClips: { color: 'var(--series-3)' },
  TeslaTrackMode: { color: 'var(--series-4)' },
  Other: { color: 'var(--series-5)' },
}
export const catLabel = (id) => t(`cat.${categories[id] ? id : 'Other'}`)
export const catShort = (id) => t(`cat.${categories[id] ? id : 'Other'}.short`)

export function reason(r) {
  if (!r) return null
  const known = `reason.${r}`
  if (t(known) !== known) return t(known)
  if (r.startsWith('sentry_aware_accel')) return t('reason.sentry_accel')
  if (r.startsWith('sentry')) return t('reason.sentry')
  const s = r.replace(/_/g, ' ')
  return s.charAt(0).toUpperCase() + s.slice(1)
}

export const cameraName = (c) => { const k = `cam.${c}`; return t(k) === k ? c : t(k) }

// Orden de las cámaras en la cuadrícula: como se ven en el coche.
export const cameraOrder = ['front', 'back', 'left_repeater', 'right_repeater', 'left_pillar', 'right_pillar']

export { i18n }
