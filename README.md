# teslapi

[Español](README.es.md)

A smart USB drive for your Tesla's dashcam, on a **Raspberry Pi Zero 2 W**. The car sees a normal USB
drive for dashcam and Sentry recordings and can format it whenever it wants. When the Pi sees a
fixed Bluetooth device at home (your EV charger, a beacon), it turns on Wi-Fi, copies the videos to
your NAS over SMB, and you can watch them in a web portal on your home network.

> Independent project, not affiliated with or endorsed by Tesla, Inc. "Tesla" is a trademark of its
> owner. Dashcam footage captures other people, and the rules differ by country: using it lawfully
> is up to you.

## How it works

```
Car ──USB──▶ configfs mass_storage ──▶ /data/cam_disk.bin (exFAT, managed by the car)
                                             │ cp --reflink (instant, USB stays connected)
                                             ▼
                       /data/snap/*.bin ─ fsck the copy ─ mount ro ─▶ CIFS ─▶ NAS/TeslaCam
BLE (BlueZ) ─ home device presence with hysteresis ─▶ Wi-Fi on/off (NetworkManager + rfkill)
teslapid (Go, one binary) ─ API + embedded Svelte portal
```

- **The car can format without breaking anything.** It only sees `cam_disk.bin`; the system and
  `/data` are never exposed. Every read detects the partition table (MBR, GPT or none) and the
  filesystem (exFAT, FAT32) again. A format changes the serial number and the archive index
  starts over.
- **Resumable archiving.** Saved → Sentry → Track → Recent, oldest first. Each file is copied as
  `.part`, its size checked and then renamed, so a power cut loses at most the file in progress.
  Encrypted clips (`EncryptedClips/`) are copied as they are.
- **Space.** Below 20 % free, and only with the car idle, the drive disconnects for a few seconds
  and deletes events after comparing them byte by byte with the NAS. `RecentClips` is never
  deleted, because the car rotates it.
- **Read-only root**, journal in RAM, tmpfs for volatile state and a trimmed boot. The portal shows
  how long the car took to see the drive on each boot.

## Security

Read [SECURITY.md](SECURITY.md) before installing. This device lives in a car and **its card holds
your Wi-Fi and NAS passwords in clear text**, so give it a dedicated NAS account and, if you can, its
own Wi-Fi network.

## Requirements

- Raspberry Pi Zero 2 W and a high-endurance microSD (128 GB recommended)
- A computer with an SD reader. `scripts/flash.sh` runs on macOS
- A NAS or computer with an SMB share, and 2.4 GHz Wi-Fi reaching the car
- Go (see `daemon/go.mod`) and Node 22 to build

## Getting started

1. Run `scripts/flash.sh /dev/diskN`. It writes a pinned, checksum-verified Raspberry Pi OS Lite
   (64-bit, Trixie) with your SSH key and home Wi-Fi, which it asks for, or reads from
   `TP_WIFI_SSID`, `TP_WIFI_PASS` and `TP_COUNTRY`. It also prints the setup network password, so
   note it down.
2. Put the card in the Zero 2 W, power it and wait for `ssh <user>@teslapi.local`.
3. Run `make provision` to create partitions, the drive image, the system and the read-only root.
   It also removes the temporary copies of the Wi-Fi password. Then `sudo reboot`.
4. Connect the Pi to the car (or a computer) through the **USB** port, not PWR, and open
   `http://teslapi.local`:
   - **With Wi-Fi configured**, the wizard asks how to detect home, for the NAS and for a portal
     password, which is required.
   - **Without Wi-Fi**, the Pi broadcasts `TeslaPi-XXXX`. Join it with your phone using the password
     from step 1 (or from `teslapi-setup-network.txt` on the boot partition), and the portal opens.
     The network turns off after 15 idle minutes, until the next boot.

## Configuration

Everything is in the portal, under Settings. Optionally, `teslapi.txt` on the boot partition (FAT,
editable from any computer) fills it in beforehand. It is read at boot **only if it changed**, and
passwords are erased from it once saved. The portal speaks English, Spanish, French, German, Italian
and Portuguese.

Home detection: **Bluetooth** (a fixed device; Wi-Fi only turns on when it sees it) or **Wi-Fi** (it is
home when it connects; the radio stays on).

To update: `make deploy`. Settings for the scripts go in environment variables or in `.deploy.env`
(see `.deploy.env.example`).

## Rescue

If you cannot reach the Pi, take out the microSD, open `teslapi.txt` on the boot partition, set
`force_wifi = 1` and put it back. Wi-Fi stays on and SSH is available. Set it back to `0` before
returning it to the car, or Wi-Fi will stay on everywhere.

## Layout

| Folder | Contents |
|---|---|
| `daemon/` | `teslapid` in Go: `diskimg`, `teslacam`, `archive`, `presence`, `wifi`, `gadget`, `snapshot`, `nas`, `core`, `api` |
| `web/` | Portal (Svelte 5 + Tailwind 4), compiled into the binary |
| `system/` | USB gadget script, systemd units, NetworkManager, SSH, firewall, `config.txt` |
| `setup/provision.sh` | Provisions the Pi (idempotent) |
| `scripts/` | `flash.sh` (writes the microSD), `deploy.sh` (builds and deploys) |
| `test/` | exFAT and FAT32 test images, and end-to-end tests |

See [CONTRIBUTING.md](CONTRIBUTING.md) to work on it.

## License

[MIT](LICENSE). The binary includes third-party software, listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
