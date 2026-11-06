#!/usr/bin/env python3
"""E2E del asistente de primer arranque contra `teslapid -dev` con el estado vacío.

Recorre el asistente en un navegador real (móvil 390x844) en francés, con una
contraseña de Wi-Fi corta (debe bloquear), un NAS inalcanzable (debe mostrar el
motivo traducido) y uno alcanzable, y comprueba la configuración resultante.

Uso: wizard.py <url> <carpeta-capturas> <host-smb-alcanzable>
"""
import base64, json, os, subprocess, sys, time, urllib.error, urllib.request
sys.path.insert(0, os.path.dirname(__file__))
from viewer_sync import CDP, CHROME

PASSWORD = "contrasena-e2e-1"
url, shots, smb_host = sys.argv[1], sys.argv[2], sys.argv[3]
os.makedirs(shots, exist_ok=True)
import shutil; shutil.rmtree("/tmp/teslapi-wizard-chrome", ignore_errors=True)
port = 9337
proc = subprocess.Popen([CHROME, "--headless=new", f"--remote-debugging-port={port}", "--user-data-dir=/tmp/teslapi-wizard-chrome",
                         "--no-first-run", "--lang=en-US", "about:blank"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
fails = []


def check(name, ok, extra=""):
    print(("OK   " if ok else "FALLO"), name, extra)
    if not ok: fails.append(name)


try:
    ws = None
    for _ in range(100):
        try:
            t = [x for x in json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/json")) if x["type"] == "page"]
            if t: ws = CDP(t[0]["webSocketDebuggerUrl"]); break
        except Exception: pass
        time.sleep(0.1)
    ws.call("Page.enable"); ws.call("Runtime.enable")
    ws.call("Network.enable")
    ws.call("Network.setUserAgentOverride", userAgent="Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", acceptLanguage="en-US,en")
    ws.call("Emulation.setLocaleOverride", locale="en-US")
    ws.call("Emulation.setEmulatedMedia", features=[{"name": "prefers-color-scheme", "value": "dark"}])
    ws.call("Emulation.setDeviceMetricsOverride", width=390, height=844, deviceScaleFactor=2, mobile=True)
    ws.call("Page.navigate", url=url)

    def text(): return ws.js("document.body.innerText") or ""
    def wait(pred, what, t=15):
        end = time.time() + t
        while time.time() < end:
            if pred(text()): return True
            time.sleep(0.25)
        check(what, False, "→ texto actual: " + text()[:200].replace("\n", " | "))
        return False
    def click(label, tag="button, a, li"):
        r = ws.js("""((l, sel) => { const el = [...document.querySelectorAll(sel)].find(e => e.innerText.trim().includes(l) && !e.disabled);
          if (!el) return false; (el.querySelector('button') && el.tagName === 'LI' ? el.querySelector('button') : el).click(); return true })(%s, %s)""" % (json.dumps(label), json.dumps(tag)))
        if not r: check(f"click «{label}»", False)
        time.sleep(0.4)
        return r
    def fill(sel, value):
        ws.js("""((s, v) => { const el = document.querySelector(s); const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
          set.call(el, v); el.dispatchEvent(new Event('input', { bubbles: true })) })(%s, %s)""" % (json.dumps(sel), json.dumps(value)))
        time.sleep(0.3)
    def disabled(label):
        return ws.js("""((l) => { const el = [...document.querySelectorAll('button')].find(e => e.innerText.trim().includes(l)); return el ? el.disabled : null })(%s)""" % json.dumps(label))
    n = [0]
    def shot(name):
        n[0] += 1
        m = ws.call("Page.getLayoutMetrics")
        h = int((m.get("cssContentSize") or m["contentSize"])["height"])
        d = ws.call("Page.captureScreenshot", format="png", captureBeyondViewport=True, clip={"x": 0, "y": 0, "width": 390, "height": min(h, 3000), "scale": 1})
        p = f"{shots}/{n[0]:02d}-{name}.png"
        open(p, "wb").write(base64.b64decode(d["data"])); print("     captura", p)

    # 1. Bienvenida: el navegador está en inglés
    wait(lambda t: "Welcome to teslapi" in t, "bienvenida en inglés (idioma del navegador)")
    check("idioma inicial = navegador (en)", "Get started" in text())
    shot("bienvenida-en")
    click("Français")
    wait(lambda t: "Bienvenue dans teslapi" in t, "cambia a francés al elegirlo")
    shot("bienvenida-fr")
    click("Commencer")

    # 2. Wi-Fi
    wait(lambda t: "MOVISTAR_4F2A" in t and "Garaje" in t, "lista las redes que ve la Pi")
    shot("wifi-lista")
    click("Garaje", "li")
    time.sleep(0.4)
    fill('input[type=password]', "corta")
    check("contraseña Wi-Fi de menos de 8 caracteres bloquea «Connecter»", disabled("Connecter") is True)
    fill('input[type=password]', "claveWifi123")
    check("con 8+ caracteres se puede continuar", disabled("Connecter") is False)
    shot("wifi-clave")
    click("Connecter")

    # 3. Detección de casa (la Pi ya está en la LAN)
    wait(lambda t: "Comment sait-il que vous êtes à la maison" in t, "pasa al paso de detección de casa")
    shot("casa")
    click("Bluetooth")
    click("Chercher")
    wait(lambda t: "Tesla Model 3" in t, "la búsqueda BLE lista dispositivos", 20)
    check("«Suivant» bloqueado sin dispositivo elegido", disabled("Suivant") is True)
    shot("casa-ble")
    click("Utiliser celui-ci")
    check("con dispositivo elegido se puede seguir", disabled("Suivant") is False)
    click("Suivant")

    # 4. NAS: primero inalcanzable
    wait(lambda t: "Où enregistrer les vidéos" in t, "paso del NAS")
    check("«Suivant» bloqueado sin probar la conexión", disabled("Suivant") is True)
    inputs = ws.js("[...document.querySelectorAll('input')].map(i => i.placeholder + '|' + i.type)")
    fill('input[placeholder="//servidor/recurso"]', "//203.0.113.9/tesla")  # TEST-NET: no responde
    click("Tester la connexion")
    wait(lambda t: "ne répond pas" in t, "NAS inalcanzable → motivo traducido al francés", 25)
    shot("nas-error")
    fill('input[placeholder="//servidor/recurso"]', f"//{smb_host}/tesla")
    click("Tester la connexion")
    wait(lambda t: "Connexion correcte" in t, "NAS alcanzable → «Connexion correcte»", 25)
    shot("nas-ok")
    check("probar con éxito habilita «Suivant»", disabled("Suivant") is False)
    click("Suivant")

    # 5. Final
    wait(lambda t: "Dernière étape" in t, "último paso")
    shot("final")
    check("sin contraseña no se puede terminar", disabled("Terminer") is True)
    fill('input[type=password]', "corta")
    check("una contraseña corta no se acepta", disabled("Terminer") is True)
    fill('input[type=password]', PASSWORD)
    check("con 8+ caracteres se puede terminar", disabled("Terminer") is False)
    click("Terminer")
    wait(lambda t: "Accueil" in t and "Prêt à enregistrer" in t or "À la maison" in t or "Prêt" in t, "entra en el portal ya en francés")
    time.sleep(1.5)
    shot("portal")

    login = urllib.request.Request(url + "/api/auth/login", data=json.dumps({"password": PASSWORD}).encode(), headers={"Content-Type": "application/json"})
    token = urllib.request.urlopen(login).headers["Set-Cookie"].split(";")[0]
    cfg = json.load(urllib.request.urlopen(urllib.request.Request(url + "/api/config", headers={"Cookie": token})))
    try:
        urllib.request.urlopen(url + "/api/config")
        check("sin sesión la API se niega", False)
    except urllib.error.HTTPError as e:
        check("sin sesión la API se niega", e.code == 401, str(e.code))
    check("setup.done = true", cfg["setup"]["done"] is True)
    check("idioma guardado = fr", cfg["language"] == "fr", cfg["language"])
    check("Wi-Fi guardada", cfg["wifi"]["ssid"] == "Garaje", cfg["wifi"]["ssid"])
    check("contraseña Wi-Fi no sale por la API", cfg["wifi"]["password"] == "••••••••")
    check("dispositivo BLE guardado", cfg["charger"]["address"] != "", cfg["charger"]["address"])
    check("NAS guardado", cfg["smb"]["share"] == f"//{smb_host}/tesla", cfg["smb"]["share"])
    check("trigger = ble", cfg["trigger"] == "ble")
finally:
    proc.terminate()
print("\nRESULTADO:", "TODO OK" if not fails else f"{len(fails)} fallos: {fails}")
sys.exit(1 if fails else 0)
