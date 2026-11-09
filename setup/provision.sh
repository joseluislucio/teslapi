#!/bin/bash
# Aprovisiona una Raspberry Pi Zero 2 W con Raspberry Pi OS Lite 64-bit (Trixie)
# recién instalado. Se ejecuta EN LA PI como root, desde el directorio que sube
# scripts/deploy.py (contiene teslapid, system/ y setup/). Es idempotente: se puede
# repetir y cada paso comprueba si ya está hecho.
#
#   sudo ./setup/provision.sh            # todo
#   sudo ./setup/provision.sh install    # binario y ficheros del sistema (actualizar)
#   sudo ./setup/provision.sh scrub      # retirar las copias de la clave Wi-Fi de cloud-init
set -euo pipefail

SRC=$(cd "$(dirname "$0")/.." && pwd)
ROOT_SIZE_GB=${ROOT_SIZE_GB:-6}
# Reserva de /data para snapshots (bloques que el coche reescribe mientras se archiva) y estado.
RESERVE_PCT=${RESERVE_PCT:-15}

log() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "ejecuta con sudo"
grep -q "Raspberry Pi Zero 2" /proc/device-tree/model 2>/dev/null || echo "Aviso: no es una Zero 2 W ($(tr -d '\0' < /proc/device-tree/model))"

remount_rw() { mount -o remount,rw / 2>/dev/null || true; mount -o remount,rw /boot/firmware 2>/dev/null || true; }

ROOTDEV=$(findmnt -no SOURCE /)            # /dev/mmcblk0p2
DISK=/dev/$(lsblk -no PKNAME "$ROOTDEV")   # /dev/mmcblk0
P=${DISK}p

step_packages() {
	log "Paquetes"
	local need=()
	for p in xfsprogs exfatprogs dosfstools cifs-utils rfkill iw bluez dnsmasq-base avahi-daemon nftables; do
		dpkg -s "$p" >/dev/null 2>&1 || need+=("$p")
	done
	if [ ${#need[@]} -gt 0 ]; then
		apt-get update -qq
		DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "${need[@]}"
	fi
}

step_partition() {
	log "Particiones (raíz ${ROOT_SIZE_GB} GB + /data XFS con reflink)"
	local dname root_start root_size
	dname=$(basename "$DISK")
	root_start=$(cat /sys/block/$dname/"$(basename "$ROOTDEV")"/start)
	root_size=$(cat /sys/block/$dname/"$(basename "$ROOTDEV")"/size)
	if [ ! -b "${P}3" ]; then
		# Sin la partición provisional de flash.py, la raíz se habrá expandido entera y
		# una ext4 montada no se puede encoger: hay que volver a grabar la tarjeta.
		local disk_sectors
		disk_sectors=$(blockdev --getsz "$DISK")
		if [ $(( root_start + root_size )) -gt $(( disk_sectors - 8*1024*1024*2 )) ]; then
			die "la raíz ocupa toda la tarjeta. Vuelve a grabarla con scripts/flash.py."
		fi
		local p3_start=$(( (root_start + ROOT_SIZE_GB*1024*1024*2 + 8191) / 8192 * 8192 ))
		echo "${p3_start},+,83" | sfdisk --force --no-reread -a "$DISK"
		partx -a "$DISK" 2>/dev/null || partx -u "$DISK"
		udevadm settle
	fi
	# La raíz crece hasta donde empieza /data (grabada mide ~2,4 GB).
	local p3_start
	p3_start=$(cat /sys/block/$dname/"$(basename "${P}3")"/start)
	if [ $(( root_start + root_size )) -lt $(( p3_start - 2048 )) ]; then
		echo ",$(( p3_start - root_start ))" | sfdisk --force --no-reread -N 2 "$DISK"
		partx -u "$DISK"
		resize2fs "$ROOTDEV"
	fi
	if ! blkid -s TYPE -o value "${P}3" | grep -q xfs; then
		# reflink=1: snapshot instantáneo de la imagen del pendrive.
		mkfs.xfs -f -L tesladata -m reflink=1 "${P}3"
	fi
	mkdir -p /data
	local uuid
	uuid=$(blkid -s UUID -o value "${P}3")
	grep -q "[[:space:]]/data[[:space:]]" /etc/fstab || \
		echo "UUID=$uuid /data xfs defaults,noatime,nodiratime 0 0" >> /etc/fstab
	mountpoint -q /data || mount /data
	mkdir -p /data/state /data/snap /data/state/nm
	chmod 700 /data/state /data/state/nm /data/snap
}

step_image() {
	log "Imagen del pendrive"
	local img=/data/cam_disk.bin
	if [ -f "$img" ]; then
		echo "Ya existe ($(du -h --apparent-size "$img" | cut -f1))"
		return
	fi
	local avail size
	avail=$(df --output=avail -B1 /data | tail -1)
	size=$(( avail * (100 - RESERVE_PCT) / 100 / 1048576 * 1048576 ))
	echo "Tamaño: $(( size / 1073741824 )) GB"
	# fallocate reserva bloques contiguos: menos fragmentación que un fichero disperso.
	fallocate -l "$size" "$img"
	chmod 600 "$img" # lleva todo lo que graba el coche
	# Igual que lo deja el coche al formatear: MBR, una partición exFAT a 1 MiB.
	echo "2048,,7" | sfdisk -q "$img"
	local loop
	loop=$(losetup --find --show --partscan "$img")
	mkfs.exfat -q -L TESLACAM "${loop}p1"
	mkdir -p /run/teslapi-init
	mount -t exfat "${loop}p1" /run/teslapi-init
	mkdir -p /run/teslapi-init/TeslaCam   # sin esta carpeta el coche no graba
	umount /run/teslapi-init
	losetup -d "$loop"
}

step_install() {
	log "Binario y ficheros del sistema"
	remount_rw
	install -d /opt/teslapi
	install -m 755 "$SRC/teslapid" /opt/teslapi/teslapid
	install -m 755 "$SRC/system/teslapi-gadget.sh" /opt/teslapi/teslapi-gadget.sh
	install -m 644 "$SRC/system/systemd/"*.service /etc/systemd/system/
	install -d /etc/NetworkManager/conf.d /etc/systemd/journald.conf.d
	install -m 644 "$SRC/system/NetworkManager/teslapi.conf" /etc/NetworkManager/conf.d/teslapi.conf
	install -m 644 "$SRC/system/journald/teslapi.conf" /etc/systemd/journald.conf.d/teslapi.conf
	install -d /etc/systemd/system.conf.d /etc/sysctl.d
	install -m 644 "$SRC/system/systemd/teslapi-watchdog.conf" /etc/systemd/system.conf.d/teslapi-watchdog.conf
	install -m 644 "$SRC/system/sysctl/99-teslapi.conf" /etc/sysctl.d/99-teslapi.conf
	[ -f /boot/firmware/teslapi.txt ] || install -m 644 "$SRC/system/boot/teslapi.txt" /boot/firmware/
	install -d /etc/NetworkManager/dnsmasq-shared.d
	install -m 644 "$SRC/system/NetworkManager/dnsmasq-shared.d/teslapi.conf" /etc/NetworkManager/dnsmasq-shared.d/teslapi.conf
	install -d /etc/ssh/sshd_config.d
	install -m 644 "$SRC/system/ssh/teslapi.conf" /etc/ssh/sshd_config.d/10-teslapi.conf
	install -m 644 "$SRC/system/nftables/teslapi.nft" /etc/nftables.conf
	chmod 700 /data/snap
	[ ! -f /data/cam_disk.bin ] || chmod 600 /data/cam_disk.bin
	local cfgtxt=/boot/firmware/config.txt
	# Lo que trae la imagen y sobra: audio, detección de cámara/pantalla, driver gráfico
	# KMS e initramfs (16 MB que cargar en cada arranque; mmc y ext4 van en el kernel).
	sed -i -E 's/^dtparam=audio=on/dtparam=audio=off/; s/^camera_auto_detect=1/camera_auto_detect=0/;
		s/^display_auto_detect=1/display_auto_detect=0/; s/^(dtoverlay=vc4-kms-v3d)/#\1/;
		s/^(max_framebuffers=)/#\1/; s/^auto_initramfs=1/auto_initramfs=0/' "$cfgtxt"
	# El arranque no usa initramfs: que apt no intente regenerarlo (con root=PARTUUID mkinitramfs
	# no encuentra el dispositivo de / y deja dpkg a medias).
	sed -i 's/^update_initramfs=yes/update_initramfs=no/' /etc/initramfs-tools/update-initramfs.conf 2>/dev/null || true
	install -d /etc/initramfs-tools/conf.d
	printf 'MODULES=most\n' > /etc/initramfs-tools/conf.d/teslapi.conf
	grep -q "^# --- teslapi ---" "$cfgtxt" || cat "$SRC/system/boot/config.txt.teslapi" >> "$cfgtxt"
	systemctl daemon-reload
	systemctl daemon-reexec # arma el watchdog de system.conf.d
	sysctl --system >/dev/null # caché de escritura corta
	systemctl enable teslapi-gadget.service teslapid.service bluetooth.service NetworkManager.service avahi-daemon.service nftables.service
	systemctl reload ssh 2>/dev/null || true
	systemctl restart nftables.service
}

# La clave Wi-Fi que flash.py dejó para el primer arranque ya no hace falta: el daemon usa sus
# propios perfiles en /data. Sin esto seguiría en claro en la partición de arranque (legible
# desde cualquier ordenador) y en las copias de cloud-init y netplan.
step_scrub() {
	log "Retirar copias de la clave Wi-Fi"
	# Solo si el daemon ya la tiene o la va a importar, para no dejar la Pi sin red.
	local cfg=/data/state/config.json txt=/boot/firmware/teslapi.txt
	if ! python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1]))["wifi"]["ssid"] else 1)' "$cfg" 2>/dev/null &&
		! grep -Eq '^wifi_ssid *= *[^ ]' "$txt" 2>/dev/null; then
		echo "La Wi-Fi aún no está en la configuración del daemon: no se toca nada."
		return
	fi
	remount_rw
	[ ! -f /boot/firmware/network-config ] || printf '{"network": {"version": 2}}\n' > /boot/firmware/network-config
	rm -f /etc/netplan/*.yaml /run/NetworkManager/system-connections/netplan-* /data/state/nm/netplan-*
	rm -f /var/lib/cloud/instance/network-config.json /var/lib/cloud/instances/*/network-config.json \
		/var/lib/cloud/instances/*/network-config /var/lib/cloud/seed/nocloud/network-config /var/lib/cloud/seed/nocloud-net/network-config
	touch /etc/cloud/cloud-init.disabled 2>/dev/null || true
	sync
}

remount_ro() { mount -o remount,ro /boot/firmware 2>/dev/null || true; mount -o remount,ro / 2>/dev/null || true; }

step_config() {
	log "Configuración inicial de teslapid"
	local cfg=/data/state/config.json
	if [ -f "$cfg" ]; then
		echo "Ya existe $cfg (se edita desde el portal)"
		return
	fi
	# Valores que pasa deploy.py por entorno; lo que falte lo pide el asistente del portal.
	python3 - "$cfg" <<'PY'
import json, os, sys
cfg = {
  "charger": {"address": os.environ.get("TP_CHARGER_ADDR", ""), "name": os.environ.get("TP_CHARGER_NAME", ""),
              "rssi_min": -85, "seen_windows": 2, "absent_after_sec": 180, "scan_on_sec": 2, "scan_period_sec": 10},
  "wifi": {"ssid": os.environ.get("TP_WIFI_SSID", ""), "password": os.environ.get("TP_WIFI_PASS", "")},
  "smb": {"share": os.environ.get("TP_SMB_SHARE", ""), "subdir": "TeslaCam", "user": os.environ.get("TP_SMB_USER", ""),
          "password": os.environ.get("TP_SMB_PASS", ""), "domain": os.environ.get("TP_SMB_DOMAIN", ""), "version": "3.1.1"},
  "archive": {"recent_clips": True, "min_clip_bytes": 100000, "settle_sec": 90, "free_space_pct": 20, "repeat_min": 30},
}
fd = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
os.write(fd, json.dumps(cfg, indent=2).encode())
os.close(fd)
PY
}

step_trim() {
	log "Servicios innecesarios"
	remount_rw
	local units=(
		ModemManager.service triggerhappy.service triggerhappy.socket
		apt-daily.timer apt-daily-upgrade.timer man-db.timer
		dphys-swapfile.service rsyslog.service keyboard-setup.service
		NetworkManager-wait-online.service e2scrub_all.timer e2scrub_reap.service
		fstrim.timer rpi-display-backlight.service sshswitch.service
		cloud-init.service cloud-init-local.service cloud-config.service cloud-final.service cloud-init-main.service cloud-init-network.service
		systemd-networkd-wait-online.service glamor-test.service
		# El swap de Trixie (archivo en la raíz + zram) impide volver a poner la raíz en solo lectura.
		var-swap.swap dev-zram0.swap systemd-zram-setup@zram0.service rpi-zram-writeback.service
		rpi-zram-writeback.timer rpi-resize-swap-file.service rpi-setup-loop@var-swap.service
	)
	for u in "${units[@]}"; do
		systemctl list-unit-files "$u" >/dev/null 2>&1 && systemctl disable --now "$u" 2>/dev/null || true
		systemctl mask "$u" 2>/dev/null || true
	done
	# cloud-init ya hizo su trabajo en el primer arranque.
	touch /etc/cloud/cloud-init.disabled 2>/dev/null || true
	command -v dphys-swapfile >/dev/null && dphys-swapfile swapoff 2>/dev/null || true
}

step_readonly() {
	log "Raíz de solo lectura"
	remount_rw
	# tmpfs para todo lo que el sistema escribe en marcha.
	for d in /tmp /var/tmp /var/log /var/lib/NetworkManager /var/lib/bluetooth /var/lib/systemd /var/lib/dhcpcd /var/spool; do
		grep -q "[[:space:]]${d}[[:space:]]" /etc/fstab || echo "tmpfs $d tmpfs nosuid,nodev,mode=0755,size=16m 0 0" >> /etc/fstab
	done
	# La hora (sin RTC) persiste en /data para no arrancar en 1970.
	mkdir -p /data/state/timesync
	grep -q "/var/lib/systemd/timesync" /etc/fstab || \
		echo "/data/state/timesync /var/lib/systemd/timesync none bind,x-systemd.requires-mounts-for=/data 0 0" >> /etc/fstab
	# El journal persiste en /data (la raíz es de solo lectura y /var/log es tmpfs).
	mkdir -p /data/journal
	chmod 755 /data/journal
	grep -q "/var/log/journal" /etc/fstab || \
		echo "/data/journal /var/log/journal none bind,x-mount.mkdir,x-systemd.requires-mounts-for=/data 0 0" >> /etc/fstab
	sed -i -E 's#^(\S+\s+/\s+ext4\s+)(\S+)#\1defaults,noatime,ro#' /etc/fstab
	sed -i -E 's#^(\S+\s+/boot/firmware\s+vfat\s+)(\S+)#\1defaults,ro#' /etc/fstab
	ln -sf /run/NetworkManager/resolv.conf /etc/resolv.conf
	local cmd=/boot/firmware/cmdline.txt
	local line
	line=$(tr -d '\n' < "$cmd")
	for opt in ro fsck.mode=skip noswap quiet "modules-load=dwc2,libcomposite"; do
		case " $line " in *" $opt "*) ;; *) line="$line $opt" ;; esac
	done
	line=$(echo "$line" | sed -E 's/(^| )rw( |$)/\1/; s/ +/ /g')
	echo "$line" > "$cmd"
	echo "cmdline: $line"
}

case "${1:-all}" in
	install)
		remount_rw
		step_packages
		step_install
		systemctl restart teslapid
		step_scrub
		remount_ro
		;;
	scrub) step_scrub; remount_ro ;;
	all)
		step_packages
		step_partition
		step_image
		step_install
		step_config
		step_trim
		step_readonly
		step_scrub
		log "Listo. Reinicia la Pi: sudo reboot"
		;;
	*) die "uso: $0 [all|install|scrub]" ;;
esac
