#!/usr/bin/env python3
"""Comprueba, sin usar flash.py, lo que ha quedado en un disco grabado con él.

Uso: check_card.py <dispositivo en crudo> <carpeta de arranque montada> <tamaño en bytes>
Lee los valores esperados de las mismas variables TP_* que recibió flash.py.
"""
import json
import os
import struct
import sys
from pathlib import Path

raw, boot, size = sys.argv[1], Path(sys.argv[2]), int(sys.argv[3])
fails = []


def check(name, ok, detail=""):
    print(("OK    " if ok else "FALLO ") + name, detail)
    if not ok:
        fails.append(name)


with open(raw, "rb", buffering=0) as f:
    mbr = f.read(512)
check("firma del MBR", mbr[510:512] == b"\x55\xaa")
types = [mbr[446 + i * 16 + 4] for i in range(4)]
starts = [struct.unpack("<I", mbr[446 + i * 16 + 8 : 446 + i * 16 + 12])[0] for i in range(4)]
counts = [struct.unpack("<I", mbr[446 + i * 16 + 12 : 446 + i * 16 + 16])[0] for i in range(4)]
check("partición 1 FAT32", types[0] == 0x0C, hex(types[0]))
check("partición 2 Linux", types[1] == 0x83, hex(types[1]))
check("partición 3 provisional Linux", types[2] == 0x83, hex(types[2]))
check("partición 3 alineada", starts[2] % 8192 == 0, str(starts[2]))
check("partición 3 llega al final", starts[2] + counts[2] == size // 512, f"{starts[2] + counts[2]} / {size // 512}")
check("partición 3 deja 6 GiB a la raíz", starts[2] >= starts[1] + 6 * 1024 * 1024 * 2)

raw_files = {n: (boot / n).read_bytes() for n in ("cmdline.txt", "user-data", "network-config", "teslapi.txt")}
for name, data in raw_files.items():
    check(f"{name} sin CRLF", b"\r" not in data)
text = {k: v.decode("utf-8") for k, v in raw_files.items()}

country = os.environ["TP_COUNTRY"].upper()
check("cmdline sin resize", " resize" not in text["cmdline.txt"])
check("cmdline con país", f"cfg80211.ieee80211_regdom={country}" in text["cmdline.txt"])
check("cmdline de una línea", text["cmdline.txt"].count("\n") == 1)

check("user-data es cloud-config", text["user-data"].startswith("#cloud-config\n"))
check("user-data con usuario", f"  - name: {os.environ['TP_USER']}\n" in text["user-data"])
check("user-data con zona horaria", f"timezone: {os.environ['TP_TIMEZONE']}\n" in text["user-data"])
key = Path(os.environ["TP_PUBKEY"]).read_text().strip()
check("user-data con la clave", json.dumps(key) in text["user-data"])

ssid, psk = os.environ["TP_WIFI_SSID"], os.environ["TP_WIFI_PASS"]
net = json.loads(text["network-config"])["network"]["wifis"]["wlan0"]
check("network-config con la clave Wi-Fi exacta", net["access-points"].get(ssid, {}).get("password") == psk)
check("network-config con país", net["regulatory-domain"] == country)

lines = dict(ln.split(" = ", 1) for ln in text["teslapi.txt"].splitlines() if " = " in ln and not ln.startswith("#"))
check("teslapi.txt con la red", lines.get("wifi_ssid") == ssid)
check("teslapi.txt con la clave", lines.get("wifi_password") == psk)
check("teslapi.txt con contraseña del asistente", len(lines.get("setup_ap_password", "")) == 16)

print("\nTODO OK" if not fails else f"\n{len(fails)} FALLOS: {fails}")
sys.exit(1 if fails else 0)
