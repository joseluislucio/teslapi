#!/bin/bash
# Graba Raspberry Pi OS Lite 64-bit (Trixie) en una microSD desde el Mac y la deja
# lista para arrancar sin pantalla: usuario con tu clave SSH, Wi-Fi, hostname
# "teslapi" y SIN expansión automática de la raíz (para poder crear /data en XFS).
#
#   scripts/flash.sh /dev/diskN
#
# Wi-Fi de casa: variables TP_WIFI_SSID / TP_WIFI_PASS, o se pregunta. Con ella la Pi se
# conecta en el primer arranque (hace falta para instalar paquetes con `make provision`);
# también queda en teslapi.txt, que la Pi importa y borra. `make provision` retira después
# la copia que cloud-init necesita para ese primer arranque.
#
# Otras variables: TP_COUNTRY (código de país de la Wi-Fi, se pregunta), TP_TIMEZONE (por
# defecto la del Mac), TP_USER, TP_PUBKEY, ROOT_SIZE_GB, TP_IMAGE_URL + TP_IMAGE_SHA256.
set -euo pipefail

# Imagen probada con este proyecto. La suma va aquí, en el repositorio: comprobarla contra un
# fichero bajado del mismo sitio que la imagen no protege de nada si ese sitio está comprometido.
IMAGE_URL=${TP_IMAGE_URL:-https://downloads.raspberrypi.com/raspios_lite_arm64/images/raspios_lite_arm64-2026-10-06/2026-10-06-raspios-trixie-arm64-lite.img.xz}
IMAGE_SHA256=${TP_IMAGE_SHA256:-483db18a48da399b5b7022ffae9a07bc6d99daab30e593aedac09b69ff422843}

DISK=${1:-}
[[ "$DISK" =~ ^/dev/disk[0-9]+$ ]] || { echo "uso: $0 /dev/diskN"; exit 1; }
IMG_DIR=~/Downloads/teslapi-img
USER_NAME=${TP_USER:-$(whoami)}
PUBKEY=${TP_PUBKEY:-~/.ssh/id_ed25519.pub}
ROOT_SIZE_GB=${ROOT_SIZE_GB:-6}

SSID=${TP_WIFI_SSID:-}
PSK=${TP_WIFI_PASS:-}
if [ -z "$SSID" ]; then
	read -r -p "Wi-Fi de casa (2,4 GHz), nombre de la red: " SSID
	[ -z "$SSID" ] || read -r -s -p "Contraseña de «${SSID}» (vacía si es abierta): " PSK
	echo
fi
[ -n "$SSID" ] || { echo "falta la Wi-Fi: sin ella no hay red en el primer arranque"; exit 1; }
COUNTRY=${TP_COUNTRY:-}
[ -n "$COUNTRY" ] || read -r -p "País de la Wi-Fi, dos letras (ES, PT, DE, GB, US…): " COUNTRY
COUNTRY=$(tr '[:lower:]' '[:upper:]' <<<"$COUNTRY")
[[ "$COUNTRY" =~ ^[A-Z]{2}$ ]] || { echo "país no válido: $COUNTRY"; exit 1; }
TIMEZONE=${TP_TIMEZONE:-$(readlink /etc/localtime | sed 's#.*/zoneinfo/##')}
[[ "$TIMEZONE" =~ ^[A-Za-z0-9_+/-]+$ ]] || { echo "zona horaria no válida: $TIMEZONE (usa TP_TIMEZONE)"; exit 1; }
[ -f "$PUBKEY" ] || { echo "no existe $PUBKEY (TP_PUBKEY=ruta a tu clave pública SSH)"; exit 1; }

# --- Comprobar que el disco es una tarjeta extraíble y no un disco del Mac ---
info=$(diskutil info "$DISK")
internal=$(awk -F': *' '/^ *Internal:/{print $2}' <<<"$info")
size=$(diskutil info -plist "$DISK" | plutil -extract TotalSize raw -)
model=$(awk -F': *' '/Media Name:/{print $2}' <<<"$info")
removable=$(awk -F': *' '/^ *Removable Media:/{print $2}' <<<"$info")
proto=$(awk -F': *' '/^ *Protocol:/{print $2}' <<<"$info")
# El lector SD integrado del Mac figura como interno: vale si el medio es una SD extraíble.
if [ "$internal" != "No" ] && ! { [ "$removable" = "Removable" ] && [ "$proto" = "Secure Digital" ]; }; then
	echo "$DISK es interno y no es una tarjeta SD extraíble: me niego."; exit 1
fi
[ "$size" -lt $((300 * 1000 * 1000 * 1000)) ] || { echo "$DISK mide más de 300 GB: no parece una microSD."; exit 1; }
echo "Disco: $DISK — $model — $((size / 1000000000)) GB"
diskutil list "$DISK"
read -r -p "Se BORRARÁ todo $DISK. Escribe BORRAR para continuar: " ok
[ "$ok" = "BORRAR" ] || { echo "Cancelado."; exit 1; }

# --- Imagen oficial, verificada ---
mkdir -p "$IMG_DIR"
XZ="$IMG_DIR/$(basename "$IMAGE_URL")"
[ -f "$XZ" ] || curl -L --fail --proto '=https' -o "$XZ" "$IMAGE_URL"
echo "$IMAGE_SHA256  $XZ" | shasum -a 256 -c -

# --- Grabar ---
diskutil unmountDisk "$DISK"
RDISK=${DISK/disk/rdisk}
echo "Grabando (unos minutos)…"
xz -dc "$XZ" | sudo dd of="$RDISK" bs=4m
sync

# --- Partición 3 provisional tras la raíz: el primer arranque ya no expande la raíz
#     (además se quita "resize" de cmdline.txt) y provision.sh la convierte en /data XFS.
MBR=$(mktemp)
# shellcheck disable=SC2024  # la redirección a un fichero propio es lo que se quiere
sudo dd if="$RDISK" bs=512 count=1 2>/dev/null >"$MBR"
python3 - "$size" "$ROOT_SIZE_GB" "$MBR" <<'PY'
import struct, sys
size, root_gb, mbr = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3]
b = bytearray(open(mbr, 'rb').read())
assert b[510:512] == b'\x55\xaa', 'MBR inválido'
p2 = b[446+16:446+32]
p2_start = struct.unpack('<I', p2[8:12])[0]
start = p2_start + root_gb * 1024 * 1024 * 2
start = (start + 8191) // 8192 * 8192
total = size // 512
e = bytearray(16)
e[4] = 0x83
e[1:4] = e[5:8] = b'\xfe\xff\xff'   # CHS no usado
e[8:12] = struct.pack('<I', start)
e[12:16] = struct.pack('<I', total - start)
b[446+32:446+48] = e
open(mbr, 'wb').write(b)
print(f"p3 provisional: sector {start} → {total} ({(total-start)*512/2**30:.1f} GiB)")
PY
diskutil unmountDisk "$DISK" >/dev/null   # macOS monta bootfs tras el volcado y el MBR queda ocupado
sudo dd if="$MBR" of="$RDISK" bs=512 count=1 conv=notrunc
rm -f "$MBR"
sync

# --- Configuración de primer arranque (cloud-init) ---
diskutil mountDisk "$DISK" >/dev/null || true
sleep 2
BOOT=/Volumes/bootfs
[ -f "$BOOT/cmdline.txt" ] || { echo "no se montó la partición de arranque"; exit 1; }
sed -i '' -E 's/ resize//; s/$/ cfg80211.ieee80211_regdom='"$COUNTRY"'/' "$BOOT/cmdline.txt"
cat > "$BOOT/user-data" <<EOF
#cloud-config
hostname: teslapi
manage_etc_hosts: true
timezone: $TIMEZONE
users:
  - name: $USER_NAME
    groups: [sudo, adm, netdev]
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "$PUBKEY")
enable_ssh: true
ssh_pwauth: false
runcmd:
  - [systemctl, enable, --now, ssh]
EOF
# Los secretos van a Python por el entorno: los argumentos se ven en ps.
TP_SSID=$SSID TP_PSK=$PSK TP_COUNTRY=$COUNTRY python3 - "$BOOT/network-config" <<'PY'
import json, os, sys
ssid, psk, country = os.environ["TP_SSID"], os.environ["TP_PSK"], os.environ["TP_COUNTRY"]
# JSON es YAML válido: así no hay problemas de comillas con la contraseña.
cfg = {"network": {"version": 2, "wifis": {"wlan0": {"dhcp4": True, "optional": True, "regulatory-domain": country,
       "access-points": {ssid: {"password": psk}}}}}}
open(sys.argv[1], "w").write(json.dumps(cfg, indent=2) + "\n")
PY
# Contraseña de la red del asistente, por si la Wi-Fi de casa falla: nunca es una red abierta.
AP_PASS=$(LC_ALL=C tr -dc 'a-km-np-zA-HJ-NP-Z2-9' </dev/urandom | head -c 16)
# teslapi.txt: la plantilla con la Wi-Fi ya rellena (lo demás se hace en el asistente).
TP_SSID=$SSID TP_PSK=$PSK TP_AP=$AP_PASS python3 - "$(dirname "$0")/../system/boot/teslapi.txt" "$BOOT/teslapi.txt" <<'PY'
import os, re, sys
src, dst = sys.argv[1:]
t = open(src).read()
for key, env in (("wifi_ssid", "TP_SSID"), ("wifi_password", "TP_PSK"), ("setup_ap_password", "TP_AP")):
    t = re.sub(r"(?m)^%s =.*$" % key, lambda m: key + " = " + os.environ[env], t)
open(dst, "w").write(t)
PY
echo "cmdline: $(cat "$BOOT/cmdline.txt")"
diskutil eject "$DISK"
echo "Listo. Mete la tarjeta en la Zero 2 W, aliméntala y en ~2 min: ssh $USER_NAME@teslapi.local"
echo
echo "Si la Pi no llega a tu Wi-Fi emitirá la red TeslaPi-XXXX. Apunta su contraseña, no se vuelve a mostrar:"
echo "    $AP_PASS"
