// Comprueba que todas las traducciones tienen las mismas claves que el español y que
// todas las claves usadas en el código existen. Falla (exit 1) si algo no cuadra.
import { readFileSync, readdirSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'src')
const langs = ['es', 'en', 'fr', 'de', 'it', 'pt']
const dict = {}
for (const l of langs) dict[l] = (await import(pathToFileURL(join(root, 'locales', `${l}.js`)).href)).default

let bad = 0
const fail = (m) => { console.error('✗ ' + m); bad++ }
const ref = Object.keys(dict.es)
for (const l of langs) {
  for (const k of ref) if (!(k in dict[l])) fail(`${l}: falta ${k}`)
  for (const k of Object.keys(dict[l])) if (!(k in dict.es)) fail(`${l}: sobra ${k}`)
  // los {marcadores} deben coincidir con los del español
  for (const k of ref) {
    const ph = (s) => [...(s || '').matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',')
    if (k in dict[l] && ph(dict[l][k]) !== ph(dict.es[k])) fail(`${l}: marcadores distintos en ${k}`)
  }
}

const files = []
const walk = (d) => { for (const e of readdirSync(d, { withFileTypes: true })) { const p = join(d, e.name); e.isDirectory() ? (e.name !== 'locales' && walk(p)) : /\.(svelte|js)$/.test(e.name) && files.push(p) } }
walk(root)
const used = new Set()
const dynamic = [/t\(`([^`$]*)\$\{[^}]*\}([^`]*)`/g]
for (const f of files) {
  if (f.endsWith('i18n.svelte.js')) continue
  const src = readFileSync(f, 'utf8')
  for (const m of src.matchAll(/(?<![\w.])(?:t|tr)\(\s*'([\w.]+)'/g)) used.add(m[1])
  for (const m of src.matchAll(/\bkey:\s*'([\w.]+)'/g)) used.add(m[1])
  for (const m of src.matchAll(/(?<![\w.])t\(`([^`]+)`/g)) {
    const re = new RegExp('^' + m[1].replace(/[.*+?^()|[\]\\]/g, '\\$&').replace(/\$\{[^}]*\}/g, '.+') + '$')
    if (!ref.some((k) => re.test(k))) fail(`${f}: ninguna clave casa con \`${m[1]}\``)
  }
}
for (const k of used) if (!(k in dict.es)) fail(`usada pero sin definir: ${k}`)
// claves cuyo uso es dinámico o viene del daemon: no se marcan como huérfanas
const dyn = /^(msg\.|cat\.|reason\.|cam\.|trigger\.)/
const orphan = ref.filter((k) => !used.has(k) && !dyn.test(k))
if (orphan.length) console.warn(`aviso: ${orphan.length} claves sin uso aparente: ${orphan.join(', ')}`)
console.log(bad ? `${bad} problemas` : `i18n OK: ${langs.length} idiomas × ${ref.length} claves`)
process.exit(bad ? 1 : 0)
