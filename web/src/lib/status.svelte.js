// Estado en vivo por Server-Sent Events, con reconexión.
export const live = $state({ status: null, connected: false, lastUpdate: 0 })

let es
export function connect() {
  es?.close()
  es = new EventSource('/api/stream')
  es.onmessage = (e) => {
    live.status = JSON.parse(e.data)
    live.connected = true
    live.lastUpdate = Date.now()
  }
  es.onerror = () => {
    live.connected = false
    es.close()
    setTimeout(connect, 3000)
  }
}

// Avisos breves (guardado, errores de acciones).
export const toasts = $state({ list: [] })
let seq = 0
export function toast(text, kind = 'info') {
  const id = ++seq
  toasts.list.push({ id, text, kind })
  setTimeout(() => { toasts.list = toasts.list.filter((t) => t.id !== id) }, kind === 'error' ? 7000 : 3500)
}

// Sesión y estado del asistente (los lee App.svelte para decidir qué pantalla enseñar).
export const auth = $state({ required: false, authenticated: true, setupDone: true, language: '', ready: false })
