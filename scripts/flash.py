#!/usr/bin/env python3
"""Graba Raspberry Pi OS Lite 64-bit (Trixie) en una microSD desde macOS, Linux o Windows.

La deja lista para arrancar sin pantalla: usuario con tu clave SSH, Wi-Fi, hostname "teslapi" y
SIN expansión automática de la raíz (para poder crear /data en XFS).

    python3 scripts/flash.py                 # lista las tarjetas que ve
    python3 scripts/flash.py /dev/diskN      # macOS
    python3 scripts/flash.py /dev/sdX        # Linux (o /dev/mmcblkN)
    py scripts\\flash.py N                    # Windows, en un terminal de administrador

Wi-Fi de casa: variables TP_WIFI_SSID / TP_WIFI_PASS, o se pregunta. Con ella la Pi se conecta en
el primer arranque (hace falta para instalar paquetes al aprovisionar); también queda en
teslapi.txt, que la Pi importa y borra. El aprovisionado retira después la copia que cloud-init
necesita para ese primer arranque.

Otras variables: TP_COUNTRY (código de país de la Wi-Fi, se pregunta), TP_TIMEZONE, TP_USER,
TP_PUBKEY, ROOT_SIZE_GB, TP_IMAGE_DIR, TP_IMAGE_URL + TP_IMAGE_SHA256.
"""
import argparse
import getpass
import hashlib
import json
import lzma
import os
import plistlib
import re
import secrets
import shutil
import string
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

# Imagen probada con este proyecto. La suma va aquí, en el repositorio: comprobarla contra un
# fichero bajado del mismo sitio que la imagen no protege de nada si ese sitio está comprometido.
IMAGE_URL = "https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2026-10-06/2026-10-06-raspios-trixie-arm64-lite.img.xz"
IMAGE_SHA256 = "483db18a48da399b5b7022ffae9a07bc6d99daab30e593aedac09b69ff422843"

REPO = Path(__file__).resolve().parent.parent
SECTOR = 512
CHUNK = 4 * 1024 * 1024
MAX_SIZE = 300 * 1000 * 1000 * 1000
AP_ALPHABET = "".join(c for c in string.ascii_letters + string.digits if c not in "lIO01o")


def die(msg):
    print(msg, file=sys.stderr)
    sys.exit(1)


# --- Lo que se escribe en la tarjeta (sin dependencias del sistema, con pruebas) ---


def patch_mbr(mbr, size, root_gb):
    """Partición 3 provisional tras la raíz: el primer arranque ya no expande la raíz (además se
    quita "resize" de cmdline.txt) y provision.sh la convierte en /data XFS."""
    b = bytearray(mbr[:SECTOR])
    if len(b) != SECTOR or b[510:512] != b"\x55\xaa":
        raise ValueError("MBR inválido")
    p2_start = struct.unpack("<I", b[446 + 16 + 8 : 446 + 16 + 12])[0]
    start = p2_start + root_gb * 1024 * 1024 * 2
    start = (start + 8191) // 8192 * 8192
    total = size // SECTOR
    if total - start < 2 * 1024 * 1024 * 2:
        raise ValueError(f"la tarjeta es demasiado pequeña: {size / 1e9:.0f} GB")
    e = bytearray(16)
    e[4] = 0x83
    e[1:4] = e[5:8] = b"\xfe\xff\xff"  # CHS no usado
    e[8:12] = struct.pack("<I", start)
    e[12:16] = struct.pack("<I", total - start)
    b[446 + 32 : 446 + 48] = e
    return bytes(b), start, total


def edit_cmdline(text, country):
    line = text.replace("\r", "").strip().replace(" resize", "")
    return f"{line} cfg80211.ieee80211_regdom={country}\n"


def user_data(user, timezone, pubkey):
    # La clave va como cadena JSON, que es YAML válido: su comentario puede llevar cualquier cosa.
    return f"""#cloud-config
hostname: teslapi
manage_etc_hosts: true
timezone: {timezone}
users:
  - name: {user}
    groups: [sudo, adm, netdev]
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    lock_passwd: true
    ssh_authorized_keys:
      - {json.dumps(pubkey)}
enable_ssh: true
ssh_pwauth: false
runcmd:
  - [systemctl, enable, --now, ssh]
"""


def network_config(ssid, psk, country):
    # JSON es YAML válido: así no hay problemas de comillas con la contraseña.
    ap = {"password": psk} if psk else {}
    cfg = {"network": {"version": 2, "wifis": {"wlan0": {"dhcp4": True, "optional": True, "regulatory-domain": country, "access-points": {ssid: ap}}}}}
    return json.dumps(cfg, indent=2) + "\n"


def teslapi_txt(template, ssid, psk, ap_password):
    t = template.replace("\r\n", "\n")
    for key, val in (("wifi_ssid", ssid), ("wifi_password", psk), ("setup_ap_password", ap_password)):
        t = re.sub(r"(?m)^%s =.*$" % key, lambda m, k=key, v=val: f"{k} = {v}", t)
    return t


def valid_user(name):
    return re.fullmatch(r"[a-z_][a-z0-9_-]{0,31}", name) is not None


def read_pubkey(path):
    text = Path(path).read_text(encoding="utf-8", errors="replace").strip()
    if "\n" in text or not re.match(r"(ssh-[\w@.-]+|ecdsa-[\w@.-]+|sk-[\w@.-]+) [A-Za-z0-9+/=]+( |$)", text):
        die(f"{path} no parece una clave pública SSH")
    return text


def ap_password():
    return "".join(secrets.choice(AP_ALPHABET) for _ in range(16))


# --- Qué discos se aceptan ---


def mac_refusal(i):
    # El lector SD integrado del Mac figura como interno: vale si el medio es una SD extraíble.
    if i.get("Internal") and not (i.get("Removable") and i.get("BusProtocol") == "Secure Digital"):
        return "es interno y no es una tarjeta SD extraíble"
    return None


def linux_refusal(d, mounts):
    critical = [m for m in mounts if m == "/" or m.startswith(("/boot", "/home", "/usr", "/var", "/etc"))]
    if critical:
        return f"tiene montado {critical[0]}: es un disco del sistema"
    if d.get("ro") in (True, 1, "1"):
        return "es de solo lectura"
    if d.get("type") != "disk" and not str(d.get("name", "")).startswith("loop"):
        return "no es un disco entero"
    if not (d.get("rm") in (True, 1, "1") or d.get("tran") == "usb" or str(d.get("name", "")).startswith("mmcblk")):
        return "no es extraíble"
    return None


def windows_refusal(d):
    if d.get("IsBoot") or d.get("IsSystem"):
        return "es un disco de arranque o de sistema"
    if d.get("IsReadOnly"):
        return "es de solo lectura"
    if d.get("BusType") not in ("USB", "SD", "MMC"):
        return f"no es extraíble (bus {d.get('BusType')})"
    return None


def hard_refusal(d):
    """Lo que ni siquiera la prueba con un disco virtual se puede saltar."""
    if d.get("IsBoot") or d.get("IsSystem"):
        return True
    return any(m == "/" or m.startswith("/boot") for m in d.get("mounts", []))


def run(*cmd, **kw):
    return subprocess.run(cmd, check=True, **kw)


def out(*cmd):
    return subprocess.run(cmd, check=True, capture_output=True).stdout


class Mac:
    example = "/dev/diskN"

    def parse(self, arg):
        return arg if re.fullmatch(r"/dev/disk\d+", arg) else None

    def _info(self, dev):
        return plistlib.loads(out("diskutil", "info", "-plist", dev))

    def info(self, dev):
        i = self._info(dev)
        return {"size": i["TotalSize"], "model": i.get("MediaName", "?"), "refusal": mac_refusal(i), "raw": i}

    def candidates(self):
        disks = plistlib.loads(out("diskutil", "list", "-plist", "physical")).get("WholeDisks", [])
        return ["/dev/" + d for d in disks]

    def raw(self, dev):
        return dev.replace("/dev/disk", "/dev/rdisk")

    def prepare(self, dev):
        run("diskutil", "unmountDisk", dev)

    def mount_boot(self, dev):
        run("diskutil", "mountDisk", dev, stdout=subprocess.DEVNULL)
        for _ in range(30):
            mp = self._info(dev + "s1").get("MountPoint")
            if mp:
                return Path(mp)
            time.sleep(1)
        die("no se montó la partición de arranque")

    def finish(self, dev, boot):
        run("diskutil", "eject", dev)


class Linux:
    example = "/dev/sdX"

    def parse(self, arg):
        return arg if re.fullmatch(r"/dev/(sd[a-z]+|mmcblk\d+|loop\d+)", arg) else None

    def _part(self, dev, n):
        return f"{dev}p{n}" if dev[-1].isdigit() else f"{dev}{n}"

    def info(self, dev):
        d = json.loads(out("lsblk", "-J", "-b", "-d", "-o", "NAME,SIZE,RM,RO,TYPE,TRAN,MODEL", dev))["blockdevices"][0]
        mounts = [m for m in out("lsblk", "-n", "-o", "MOUNTPOINT", dev).decode().split("\n") if m.strip()]
        d["mounts"] = mounts
        return {"size": int(d["size"]), "model": (d.get("model") or "?").strip(), "refusal": linux_refusal(d, mounts), "raw": d, "mounts": mounts}

    def candidates(self):
        rows = json.loads(out("lsblk", "-J", "-d", "-o", "NAME,TYPE"))["blockdevices"]
        return ["/dev/" + r["name"] for r in rows if r.get("type") == "disk"]

    def raw(self, dev):
        return dev

    def prepare(self, dev):
        for m in self.info(dev)["mounts"]:
            run("sudo", "umount", m)

    def mount_boot(self, dev):
        subprocess.run(["sudo", "partprobe", dev], check=False, stderr=subprocess.DEVNULL)
        subprocess.run(["sudo", "blockdev", "--rereadpt", dev], check=False, stderr=subprocess.DEVNULL)
        subprocess.run(["udevadm", "settle"], check=False)
        part = self._part(dev, 1)
        for _ in range(30):
            if os.path.exists(part):
                break
            time.sleep(1)
        else:
            die(f"no aparece {part}")
        subprocess.run(["sudo", "umount", part], check=False, stderr=subprocess.DEVNULL)
        mp = tempfile.mkdtemp(prefix="teslapi-boot-")
        run("sudo", "mount", "-t", "vfat", "-o", f"uid={os.getuid()},gid={os.getgid()},umask=077", part, mp)
        return Path(mp)

    def finish(self, dev, boot):
        run("sync")
        run("sudo", "umount", str(boot))
        os.rmdir(boot)


class Windows:
    example = "N (el número de disco)"

    def _ps(self, script):
        r = subprocess.run(
            ["powershell", "-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding=[Text.Encoding]::UTF8;$ErrorActionPreference='Stop';" + script],
            capture_output=True,
        )
        if r.returncode:
            die("PowerShell: " + r.stderr.decode("utf-8", "replace")[:600])
        return r.stdout.decode("utf-8", "replace").strip()

    def parse(self, arg):
        m = re.fullmatch(r"(?:\\\\\.\\)?(?:PhysicalDrive)?(\d+)", arg, re.I)
        return m.group(1) if m else None

    def _disks(self):
        script = "Get-Disk | Select-Object Number,FriendlyName,Size,@{n='BusType';e={\"$($_.BusType)\"}},IsBoot,IsSystem,IsReadOnly | ConvertTo-Json -Compress"
        data = json.loads(self._ps(script) or "[]")
        return data if isinstance(data, list) else [data]

    def info(self, dev):
        for d in self._disks():
            if str(d["Number"]) == dev:
                return {"size": int(d["Size"]), "model": d.get("FriendlyName") or "?", "refusal": windows_refusal(d), "raw": d}
        die(f"no existe el disco {dev}")

    def candidates(self):
        return [str(d["Number"]) for d in self._disks()]

    def raw(self, dev):
        return rf"\\.\PhysicalDrive{dev}"

    def _diskpart(self, *lines):
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False) as f:
            f.write("\n".join(lines) + "\n")
        try:
            r = subprocess.run(["diskpart", "/s", f.name], capture_output=True)
            if r.returncode:
                die("diskpart: " + r.stdout.decode("utf-8", "replace")[-600:])
        finally:
            os.unlink(f.name)

    def prepare(self, dev):
        # Windows no deja escribir sobre un volumen montado: se vacía la tabla de particiones.
        # La nueva no se escribe hasta el final, para que no vuelva a montarlo a mitad.
        self._diskpart(f"select disk {dev}", "attributes disk clear readonly noerr", "online disk noerr", "clean")

    def mount_boot(self, dev):
        self._diskpart("rescan")
        script = (
            f"$p=Get-Partition -DiskNumber {dev} -PartitionNumber 1;"
            "if(-not ([string]$p.DriveLetter).Trim([char]0)){$p|Add-PartitionAccessPath -AssignDriveLetter;"
            f"$p=Get-Partition -DiskNumber {dev} -PartitionNumber 1}};"
            "([string]$p.DriveLetter).Trim([char]0)"
        )
        last = ""
        for _ in range(20):
            r = subprocess.run(["powershell", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop';" + script], capture_output=True)
            letter = r.stdout.decode("utf-8", "replace").strip()
            last = r.stderr.decode("utf-8", "replace")[:300]
            if r.returncode == 0 and letter and Path(letter + ":\\cmdline.txt").exists():
                return Path(letter + ":\\")
            time.sleep(2)
        die("no se montó la partición de arranque: " + last)

    def finish(self, dev, boot):
        letter = str(boot)[0]
        self._ps(f"Write-VolumeCache -DriveLetter {letter};Remove-PartitionAccessPath -DiskNumber {dev} -PartitionNumber 1 -AccessPath '{letter}:\\'")


def platform_backend():
    if sys.platform == "darwin":
        return Mac()
    if sys.platform.startswith("linux"):
        return Linux()
    if sys.platform == "win32":
        return Windows()
    die(f"sistema no soportado: {sys.platform}")


# --- Imagen y escritura ---


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(CHUNK), b""):
            h.update(chunk)
    return h.hexdigest()


def download(url, dest):
    if not url.startswith("https://"):
        die("la imagen debe bajarse por https")
    part = dest.with_name(dest.name + ".part")
    print(f"Descargando {url}")
    try:
        with urllib.request.urlopen(url, timeout=60) as r, open(part, "wb") as f:
            shutil.copyfileobj(r, f, CHUNK)
    except urllib.error.URLError as e:
        # Python de python.org en Mac viene sin certificados raíz: curl usa los del sistema.
        curl = shutil.which("curl")
        if not curl or "CERTIFICATE" not in str(e).upper():
            die(f"no se pudo descargar la imagen: {e}")
        run(curl, "-L", "--fail", "--proto", "=https", "-o", str(part), url)
    part.replace(dest)


def fetch_image():
    url = os.environ.get("TP_IMAGE_URL") or IMAGE_URL
    want = os.environ.get("TP_IMAGE_SHA256") or IMAGE_SHA256
    folder = Path(os.environ.get("TP_IMAGE_DIR") or Path.home() / "Downloads" / "teslapi-img")
    folder.mkdir(parents=True, exist_ok=True)
    xz = folder / url.rsplit("/", 1)[-1]
    if not xz.exists():
        download(url, xz)
    print("Comprobando la suma de la imagen…")
    got = sha256_file(xz)
    if got != want:
        die(f"la suma de {xz} no coincide. Bórralo y vuelve a intentarlo.\n  esperada {want}\n  obtenida {got}")
    return xz


def write_image(raw, xz, size, root_gb):
    """Vuelca la imagen con la tabla de particiones a cero y la escribe la última. Hasta entonces
    ningún sistema ve particiones, así que ninguno monta nada ni bloquea el disco a mitad."""
    written = 0
    h = hashlib.sha256()
    last = time.monotonic()
    with lzma.open(xz) as src, open(raw, "r+b", buffering=0) as dst:
        first = src.read(CHUNK)
        mbr, start, total = patch_mbr(first[:SECTOR], size, root_gb)
        print(f"p3 provisional: sector {start} → {total} ({(total - start) * SECTOR / 2**30:.1f} GiB)")
        chunk = bytes(SECTOR) + first[SECTOR:]
        while chunk:
            if len(chunk) % SECTOR:
                chunk += bytes(SECTOR - len(chunk) % SECTOR)
            dst.write(chunk)
            h.update(chunk)
            written += len(chunk)
            if time.monotonic() - last > 5:
                print(f"  {written / 1e9:.2f} GB grabados", flush=True)
                last = time.monotonic()
            chunk = src.read(CHUNK)
        if sys.platform != "win32":
            os.fsync(dst.fileno())
    if sys.platform.startswith("linux"):
        subprocess.run(["blockdev", "--flushbufs", raw], check=False)

    print("Comprobando lo grabado…", flush=True)
    check = hashlib.sha256()
    left = written
    with open(raw, "rb", buffering=0) as f:
        while left:
            b = f.read(min(CHUNK, left))
            if not b:
                break
            check.update(b)
            left -= len(b)
    if left or check.digest() != h.digest():
        die("lo que hay en la tarjeta no coincide con la imagen: puede estar dañada")

    with open(raw, "r+b", buffering=0) as dst:
        dst.write(mbr)
        if sys.platform != "win32":
            os.fsync(dst.fileno())
    with open(raw, "rb", buffering=0) as f:
        if f.read(SECTOR) != mbr:
            die("no se pudo escribir la tabla de particiones")
    print(f"Grabados {written / 1e9:.2f} GB y comprobados.", flush=True)


def is_windows_admin():
    import ctypes

    return bool(ctypes.windll.shell32.IsUserAnAdmin())


def write_elevated(raw, xz, size, root_gb):
    if sys.platform == "win32":
        write_image(raw, str(xz), size, root_gb)
    else:
        print("Hace falta sudo para escribir en la tarjeta.")
        run("sudo", sys.executable, str(Path(__file__).resolve()), "--write-image", raw, str(xz), str(size), str(root_gb))


def detect_timezone():
    try:
        real = os.path.realpath("/etc/localtime")
        if "zoneinfo/" in real:
            return real.split("zoneinfo/", 1)[1]
        return Path("/etc/timezone").read_text().strip()
    except OSError:
        return ""


def clean(text):
    return re.search(r"[\x00-\x1f\x7f]", text) is None


def show_list(pf):
    found = []
    for dev in pf.candidates():
        try:
            i = pf.info(dev)
        except (subprocess.CalledProcessError, KeyError, IndexError, ValueError):
            continue
        if not i["refusal"] and i["size"] and i["size"] < MAX_SIZE:
            found.append(f"  {dev}  {i['model']}  {i['size'] / 1e9:.0f} GB")
    print("Tarjetas que veo:" if found else "No veo ninguna tarjeta extraíble.")
    print("\n".join(found))
    print(f"\nUso: {Path(sys.argv[0]).name} {pf.example}")


def write_file(path, text):
    # Sin traducir saltos de línea: en Windows acabarían en CRLF y la Pi los lee con LF.
    with open(path, "wb") as f:
        f.write(text.encode("utf-8"))
        f.flush()
        os.fsync(f.fileno())


def main():
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
        sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    if len(sys.argv) == 6 and sys.argv[1] == "--write-image":
        write_image(sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5]))
        return

    p = argparse.ArgumentParser(description="Graba la microSD de teslapi")
    p.add_argument("disk", nargs="?", help="/dev/diskN (macOS), /dev/sdX o /dev/mmcblkN (Linux), N (Windows)")
    args = p.parse_args()
    pf = platform_backend()
    if not args.disk:
        show_list(pf)
        sys.exit(1)
    dev = pf.parse(args.disk)
    if dev is None:
        die(f"disco no válido: {args.disk}. Uso: {pf.example}")
    if sys.platform == "win32" and not is_windows_admin():
        die("Abre el terminal como administrador: Windows no deja escribir en el disco de otra forma.")

    user = (os.environ.get("TP_USER") or getpass.getuser()).lower()
    if not valid_user(user):
        die(f"«{user}» no vale como usuario de Linux: usa TP_USER=nombre (minúsculas, sin espacios)")
    root_gb = int(os.environ.get("ROOT_SIZE_GB") or 6)

    ssid = os.environ.get("TP_WIFI_SSID", "")
    psk = os.environ.get("TP_WIFI_PASS", "")
    if not ssid:
        ssid = input("Wi-Fi de casa (2,4 GHz), nombre de la red: ")
        if ssid:
            psk = getpass.getpass(f"Contraseña de «{ssid}» (vacía si es abierta): ")
    if not ssid:
        die("falta la Wi-Fi: sin ella no hay red en el primer arranque")
    if not clean(ssid) or not clean(psk):
        die("el nombre o la contraseña de la Wi-Fi llevan caracteres de control")
    country = (os.environ.get("TP_COUNTRY") or input("País de la Wi-Fi, dos letras (ES, PT, DE, GB, US…): ")).upper()
    if not re.fullmatch(r"[A-Z]{2}", country):
        die(f"país no válido: {country}")
    timezone = os.environ.get("TP_TIMEZONE") or detect_timezone() or input("Zona horaria (por ejemplo Europe/Madrid): ")
    if not re.fullmatch(r"[A-Za-z0-9_+/-]+", timezone):
        die(f"zona horaria no válida: {timezone} (usa TP_TIMEZONE)")
    keydir = Path.home() / ".ssh"
    pubkey_path = os.environ.get("TP_PUBKEY") or next((str(k) for k in (keydir / "id_ed25519.pub", keydir / "id_rsa.pub") if k.exists()), "")
    if not pubkey_path or not Path(pubkey_path).exists():
        die("no encuentro tu clave pública SSH. Créala con ssh-keygen -t ed25519, o usa TP_PUBKEY=ruta")
    pubkey = read_pubkey(pubkey_path)

    # --- Comprobar que el disco es una tarjeta extraíble y no uno del ordenador ---
    info = pf.info(dev)
    refusal = info["refusal"]
    if refusal and os.environ.get("TP_FLASH_TEST_DISK") == dev and not hard_refusal({**info["raw"], "mounts": info.get("mounts", [])}):
        print(f"Prueba con un disco virtual: se acepta {dev} aunque {refusal}.")
        refusal = None
    if refusal:
        die(f"{dev} {refusal}: me niego.")
    if info["size"] >= MAX_SIZE:
        die(f"{dev} mide más de 300 GB: no parece una microSD.")
    print(f"Disco: {dev} — {info['model']} — {info['size'] / 1e9:.0f} GB")
    if input(f"Se BORRARÁ todo {dev}. Escribe BORRAR para continuar: ").strip() != "BORRAR":
        die("Cancelado.")

    xz = fetch_image()
    # Antes de tocar el disco: en Windows, preparar ya vacía su tabla de particiones.
    with lzma.open(xz) as src:
        try:
            patch_mbr(src.read(SECTOR), info["size"], root_gb)
        except ValueError as e:
            die(f"{e}. No he tocado {dev}.")
    pf.prepare(dev)
    print("Grabando (unos minutos)…", flush=True)
    write_elevated(pf.raw(dev), xz, info["size"], root_gb)

    # --- Configuración de primer arranque (cloud-init) ---
    boot = pf.mount_boot(dev)
    cmdline = boot / "cmdline.txt"
    if not cmdline.exists():
        die("no se montó la partición de arranque")
    new_cmdline = edit_cmdline(cmdline.read_bytes().decode("utf-8"), country)
    write_file(cmdline, new_cmdline)
    write_file(boot / "user-data", user_data(user, timezone, pubkey))
    write_file(boot / "network-config", network_config(ssid, psk, country))
    # Contraseña de la red del asistente, por si la Wi-Fi de casa falla: nunca es una red abierta.
    ap = ap_password()
    template = (REPO / "system" / "boot" / "teslapi.txt").read_bytes().decode("utf-8")
    write_file(boot / "teslapi.txt", teslapi_txt(template, ssid, psk, ap))
    print(f"cmdline: {new_cmdline.strip()}")
    pf.finish(dev, boot)

    print(f"Listo. Mete la tarjeta en la Zero 2 W, aliméntala y en ~2 min: ssh {user}@teslapi.local")
    print()
    print("Si la Pi no llega a tu Wi-Fi emitirá la red TeslaPi-XXXX. Apunta su contraseña, no se vuelve a mostrar:")
    print(f"    {ap}")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        die(f"Ha fallado un paso ({e.returncode}): {' '.join(map(str, e.cmd[:4]))}…")
    except KeyboardInterrupt:
        die("\nCancelado. Si ya estaba grabando, la tarjeta queda a medias: vuelve a ejecutarlo.")
