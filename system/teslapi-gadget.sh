#!/bin/sh
# Publica /data/cam_disk.bin como pendrive USB en cuanto /data está montado.
# Va antes que la red, el Bluetooth y el daemon: el coche debe verlo cuanto antes.
set -eu
IMG=/data/cam_disk.bin
G=/sys/kernel/config/usb_gadget/teslapi
STATE=/data/state

[ -f "$IMG" ] || { echo "teslapi-gadget: falta $IMG" >&2; exit 1; }
mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
[ -d /sys/kernel/config/usb_gadget ] || modprobe libcomposite

if [ ! -d "$G" ]; then
	mkdir "$G"
	cd "$G"
	echo 0x1d6b > idVendor   # Linux Foundation
	echo 0x0104 > idProduct  # Multifunction Composite Gadget
	echo 0x0100 > bcdDevice
	echo 0x0200 > bcdUSB
	mkdir -p strings/0x409
	echo "teslapi" > strings/0x409/manufacturer
	echo "TeslaPi Drive" > strings/0x409/product
	awk '/Serial/ {print $3}' /proc/cpuinfo > strings/0x409/serialnumber
	mkdir -p configs/c.1/strings/0x409
	echo "TeslaCam" > configs/c.1/strings/0x409/configuration
	echo 250 > configs/c.1/MaxPower
	mkdir functions/mass_storage.0
	echo 1 > functions/mass_storage.0/stall
	L=functions/mass_storage.0/lun.0
	echo 0 > $L/cdrom
	echo 0 > $L/ro
	echo 1 > $L/removable
	echo 0 > $L/nofua        # respetar FUA: lo que el coche marca como "a disco" va a disco
	echo "TeslaPi CAM" > $L/inquiry_string
	echo "$IMG" > $L/file
	ln -s functions/mass_storage.0 configs/c.1/
fi
ls /sys/class/udc > "$G/UDC"

# Segundos desde el arranque hasta aquí: el portal enseña el histórico.
mkdir -p "$STATE"
awk '{print $1}' /proc/uptime >> "$STATE/boot_times"
tail -n 50 "$STATE/boot_times" > "$STATE/boot_times.tmp" && mv "$STATE/boot_times.tmp" "$STATE/boot_times"
