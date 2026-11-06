import { tm } from './format.js'
import { auth } from './status.svelte.js'
export { auth }

// Acceso a la API de teslapid. Los errores llevan el mensaje del servidor traducido,
// para que el motivo de un fallo llegue a la pantalla.
export class ApiError extends Error {
  constructor(status, body) {
    super(tm(body) || `Error ${status}`)
    this.status = status
    this.code = body?.code
  }
}

export async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  let data = null
  try { data = await res.json() } catch { /* sin cuerpo */ }
  if (!res.ok) {
    if (res.status === 401 && data?.error?.code === 'auth.required') auth.authenticated = false
    throw new ApiError(res.status, data?.error)
  }
  return data
}

export const get = (p) => api('GET', p)
export const post = (p, b) => api('POST', p, b)
export const put = (p, b) => api('PUT', p, b)

// URL de un fichero del pendrive ("pi") o del NAS ("nas").
export const mediaURL = (source, rel) =>
  `/media/${source}/${rel.split('/').map(encodeURIComponent).join('/')}`
