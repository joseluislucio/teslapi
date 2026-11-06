// Idiomas del portal. El orden de elección: el de la persona (guardado en este
// navegador) > el de la Pi (ajuste/asistente) > el del navegador > inglés.
import es from '../locales/es.js'
import en from '../locales/en.js'
import fr from '../locales/fr.js'
import de from '../locales/de.js'
import it from '../locales/it.js'
import pt from '../locales/pt.js'

export const locales = [
  { id: 'es', name: 'Español', tag: 'es-ES' },
  { id: 'en', name: 'English', tag: 'en-GB' },
  { id: 'fr', name: 'Français', tag: 'fr-FR' },
  { id: 'de', name: 'Deutsch', tag: 'de-DE' },
  { id: 'it', name: 'Italiano', tag: 'it-IT' },
  { id: 'pt', name: 'Português', tag: 'pt-PT' },
]
const dicts = { es, en, fr, de, it, pt }
const ids = locales.map((l) => l.id)

export const i18n = $state({ locale: 'en' })

const stored = () => { try { return localStorage.getItem('lang') } catch { return null } }

export function pick(serverLang) {
  const nav = (navigator.languages || [navigator.language]).map((l) => l?.slice(0, 2).toLowerCase())
  return [stored(), serverLang, ...nav].find((l) => ids.includes(l)) || 'en'
}

// remember=true: lo ha elegido la persona en este navegador.
export function setLocale(id, remember = false) {
  if (!ids.includes(id)) return
  i18n.locale = id
  document.documentElement.lang = id
  if (remember) try { localStorage.setItem('lang', id) } catch { /* sin almacenamiento */ }
}

export const tag = () => locales.find((l) => l.id === i18n.locale)?.tag || 'en-GB'

// t('clave', { n: 3 }) sustituye {n}. Si falta en el idioma, usa inglés y, si no, la clave.
export function t(key, args) {
  let s = dicts[i18n.locale]?.[key] ?? en[key] ?? key
  if (args) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in args ? args[k] : m))
  return s
}
