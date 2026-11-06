#!/usr/bin/env python3
"""E2E del visor: reproduce un evento en Chrome headless y comprueba que las
cámaras avanzan juntas, que saltar a otro minuto carga ese segmento y que al
acabar un segmento pasa solo al siguiente.

Uso: viewer_sync.py <url-base> <dir> <id>
  p.ej. viewer_sync.py http://localhost:8080 TeslaCam/SavedClips/<evento> <evento>
Entra en el portal con TESLAPI_E2E_PASSWORD (si aún no hay contraseña, la pone).
Sale con código 1 si alguna comprobación falla.
"""
import base64, json, os, socket, subprocess, sys, time, urllib.parse, urllib.request

CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"


class CDP:
    def __init__(self, url):
        hostport, _, path = url[5:].partition("/")
        host, _, port = hostport.partition(":")
        self.s = socket.create_connection((host, int(port)), timeout=60)
        key = base64.b64encode(os.urandom(16)).decode()
        self.s.sendall((f"GET /{path} HTTP/1.1\r\nHost: {hostport}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                        f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
        buf = b""
        while b"\r\n\r\n" not in buf:
            buf += self.s.recv(4096)
        self.buf, self.id = buf.split(b"\r\n\r\n", 1)[1], 0

    def _n(self, n):
        while len(self.buf) < n:
            self.buf += self.s.recv(65536)
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def call(self, method, **params):
        self.id += 1
        p = json.dumps({"id": self.id, "method": method, "params": params}).encode()
        mask = os.urandom(4)
        n = len(p)
        hdr = b"\x81" + (bytes([0x80 | n]) if n < 126 else bytes([0x80 | 126]) + n.to_bytes(2, "big"))
        self.s.sendall(hdr + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(p)))
        while True:
            self._n(1)
            ln = self._n(1)[0] & 0x7F
            if ln == 126: ln = int.from_bytes(self._n(2), "big")
            elif ln == 127: ln = int.from_bytes(self._n(8), "big")
            msg = json.loads(self._n(ln) or b"{}")
            if msg.get("id") == self.id:
                return msg.get("result", {})

    def js(self, expr):
        r = self.call("Runtime.evaluate", expression=expr, returnByValue=True, awaitPromise=True)
        return r.get("result", {}).get("value")


STATE = """(() => [...document.querySelectorAll('video')].map(v => ({
  src: decodeURIComponent(v.currentSrc.split('/').pop()), t: v.currentTime, paused: v.paused, ready: v.readyState })))()"""


# Inicia sesión desde la propia página: la cookie tiene que quedar en el navegador.
LOGIN = """(async (password) => {
  const send = (path, body) => fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const a = await (await fetch('/api/auth')).json()
  const r = a.required ? await send('/api/auth/login', { password }) : await send('/api/setup/finish', { password })
  if (!r.ok) return 'login: ' + r.status
  const f = await fetch('/api/refresh', { method: 'POST' })
  return f.ok ? 'ok' : 'refresh: ' + f.status
})(%s)"""


def main():
    base, d, i = sys.argv[1:4]
    password = os.environ.get("TESLAPI_E2E_PASSWORD", "desarrollo-e2e")
    url = f"{base}/#/clip/pi/{urllib.parse.quote(d, safe='')}/{urllib.parse.quote(i, safe='')}"
    port = 9334
    proc = subprocess.Popen([CHROME, "--headless=new", f"--remote-debugging-port={port}", "--user-data-dir=/tmp/teslapi-e2e-chrome",
                             "--no-first-run", "--autoplay-policy=no-user-gesture-required", "--window-size=1280,1000", "about:blank"],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    fails = []
    def check(ok, what):
        print(("OK   " if ok else "FALLO") + " " + what)
        if not ok: fails.append(what)
    try:
        ws = None
        for _ in range(100):
            try:
                t = [x for x in json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/json")) if x["type"] == "page"]
                if t: ws = CDP(t[0]["webSocketDebuggerUrl"]); break
            except Exception: pass
            time.sleep(0.1)
        ws.call("Page.enable"); ws.call("Runtime.enable")
        ws.call("Page.navigate", url=base + "/")
        time.sleep(1)
        # El portal solo enseña clips tras leer el pendrive: se pide aquí, no se da por hecho.
        r = ws.js(LOGIN % json.dumps(password))
        check(r == "ok", f"entra en el portal ({r})")
        ws.call("Page.navigate", url=url)
        ws.call("Page.reload")
        for _ in range(60):
            st = ws.js(STATE) or []
            if len(st) >= 4 and all(v["ready"] >= 2 for v in st): break
            time.sleep(0.25)
        check(len(st) >= 4 and all(v["ready"] >= 2 for v in st), f"las {len(st)} cámaras cargan el primer minuto")

        # Caso negativo: sin pulsar reproducir, el tiempo no avanza.
        time.sleep(1.5)
        st = ws.js(STATE)
        check(all(v["paused"] and v["t"] < 0.2 for v in st), "en pausa no avanza")

        ws.js("document.querySelector('[aria-label=Reproducir]').click()")
        time.sleep(4)
        st = ws.js(STATE)
        ts = [v["t"] for v in st]
        check(min(ts) > 2.5, f"reproduce: tiempos {[round(x, 2) for x in ts]}")
        check(max(ts) - min(ts) < 0.35, f"sincronizadas: desfase {max(ts) - min(ts):.3f} s")

        # Saltar al 4.º minuto con su botón.
        ws.js("[...document.querySelectorAll('aside button')].filter(b => /^\\d\\d:\\d\\d$/.test(b.textContent.trim()))[3].click()")
        time.sleep(3)
        st = ws.js(STATE)
        segs = {v["src"][:19] for v in st}
        check(len(segs) == 1, f"todas las cámaras en el mismo minuto: {segs}")
        ts = [v["t"] for v in st]
        check(not any(v["paused"] for v in st) and max(ts) - min(ts) < 0.35, f"tras saltar siguen reproduciendo juntas: {[round(x, 2) for x in ts]}")
        jumped = segs.pop() if segs else ""

        # Paso natural al siguiente minuto: situarse a 1,5 s del final del segmento actual.
        ws.js("(() => { const v = document.querySelector('video'); for (const x of document.querySelectorAll('video')) x.currentTime = v.duration - 1.5 })()")
        time.sleep(4.5)
        st = ws.js(STATE)
        nxt = {v["src"][:19] for v in st}
        check(len(nxt) == 1 and nxt.pop() > jumped, f"pasa solo al minuto siguiente ({jumped} → {[v['src'][:19] for v in st][0]})")
        check(not any(v["paused"] for v in st), "y sigue reproduciendo")
    finally:
        proc.terminate()
    print(f"\n{'TODO OK' if not fails else f'{len(fails)} FALLOS'}")
    sys.exit(1 if fails else 0)


if __name__ == "__main__":
    main()
