// Precomprime el build (brotli + gzip): la Zero 2 W no debe comprimir al vuelo.
import { readdirSync, readFileSync, writeFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { brotliCompressSync, gzipSync, constants } from 'node:zlib'

const dir = fileURLToPath(new URL('../../daemon/internal/api/dist/', import.meta.url))
const exts = /\.(html|js|css|svg|json|webmanifest)$/
let n = 0
function walk(d) {
  for (const f of readdirSync(d)) {
    const p = join(d, f)
    if (statSync(p).isDirectory()) { walk(p); continue }
    if (!exts.test(f)) continue
    const b = readFileSync(p)
    if (b.length < 1024) continue
    writeFileSync(p + '.br', brotliCompressSync(b, { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } }))
    writeFileSync(p + '.gz', gzipSync(b, { level: 9 }))
    n++
  }
}
walk(dir)
writeFileSync(join(dir, '.keep'), '')
console.log(`precomprimidos ${n} ficheros`)
