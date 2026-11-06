#!/usr/bin/env python3
"""Captura del portal con el tema y el ancho reales (también móvil por CDP,
porque Chrome headless no baja de ~500 px con --window-size).

Uso: shot.py <url> <out.png> [--scheme dark|light] [--mobile] [--eval JS]
"""
import base64, json, subprocess, sys, time, urllib.request
sys.path.insert(0, __import__("os").path.dirname(__file__))
from viewer_sync import CDP, CHROME

a = sys.argv[1:]
url, out = a[0], a[1]
scheme = a[a.index("--scheme") + 1] if "--scheme" in a else "dark"
mobile = "--mobile" in a
w, h = (390, 844) if mobile else (1280, 900)
port = 9335
proc = subprocess.Popen([CHROME, "--headless=new", f"--remote-debugging-port={port}", "--user-data-dir=/tmp/teslapi-shot-chrome",
                         "--no-first-run", "--window-size=1280,900", "about:blank"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    ws = None
    for _ in range(100):
        try:
            t = [x for x in json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/json")) if x["type"] == "page"]
            if t: ws = CDP(t[0]["webSocketDebuggerUrl"]); break
        except Exception: pass
        time.sleep(0.1)
    ws.call("Page.enable"); ws.call("Runtime.enable")
    ws.call("Emulation.setAutoDarkModeOverride", enabled=False)
    ws.call("Emulation.setEmulatedMedia", features=[{"name": "prefers-color-scheme", "value": scheme}])
    ws.call("Emulation.setDeviceMetricsOverride", width=w, height=h, deviceScaleFactor=2 if mobile else 1, mobile=mobile)
    ws.call("Page.navigate", url=url)
    time.sleep(2.5)
    if "--eval" in a:
        ws.js(a[a.index("--eval") + 1]); time.sleep(3)
    iw = ws.js("innerWidth")
    m = ws.call("Page.getLayoutMetrics")
    ch = int((m.get("cssContentSize") or m["contentSize"])["height"])
    shot = ws.call("Page.captureScreenshot", format="png", captureBeyondViewport=True,
                   clip={"x": 0, "y": 0, "width": w, "height": min(ch, 6000), "scale": 1})
    open(out, "wb").write(base64.b64decode(shot["data"]))
    print(f"OK {out} innerWidth={iw} alto={ch} scheme={scheme}")
finally:
    proc.terminate()
