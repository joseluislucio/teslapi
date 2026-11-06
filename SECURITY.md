# Security

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's **Report a vulnerability** button on the
**Security** tab of this repository. Include what you did, what happened and which version you
run (`teslapid` prints it at startup and the portal shows it on the System page).

This is a personal project maintained in spare time, so there is no response deadline, and only
the latest release gets fixes.

## What teslapi protects, and what it does not

teslapi sits in a car, in a glovebox. **Assume someone can take it.** The design goal is that
losing it costs you as little as possible, not that the card's contents stay secret.

| Threat | Status |
|---|---|
| Someone steals the Pi or the microSD | **Not protected.** Wi-Fi password, NAS password and videos are readable from the card. It boots unattended, so disk encryption would need the key on the same device. See "Protect yourself" below. |
| Another device on your network | Portal password required, sessions signed, secrets never returned by the API, and a stored password is only sent to the server it was saved for. |
| A website open on a computer in your network | `Host`, `Origin` and `Content-Type` are checked, so cross-site requests and DNS rebinding are rejected. |
| Someone listening on your network | **Not protected by default.** The portal uses plain HTTP unless you add a certificate (see below), and SMB traffic is only encrypted if you enable it. |
| Someone within Wi-Fi range during setup | The setup network is always WPA2 with a per-device password and turns itself off after 15 idle minutes. Until a portal password exists, anyone who has reached the portal can set it, so finish setup promptly. |
| Someone who can write to the USB drive or the NAS | File contents are treated as untrusted: only known file types open in the browser, a strict CSP applies, and the destination cannot be left through symlinks. A crafted filesystem image still reaches the kernel's exFAT driver and `fsck` as root. |
| Someone faking your Bluetooth device | **Not protected.** BLE offers no authentication. A fake device makes the Pi turn on its Wi-Fi and try to join, which reveals material for an offline attack on a weak Wi-Fi password. It cannot do more than that. |
| A compromised daemon | Partly contained. The service runs with reduced capabilities and `NoNewPrivileges`, but it is still root because it mounts filesystems and drives the USB gadget, and it can reach systemd over D-Bus, so this is not a hard boundary. |

Privacy notes: map tiles come from OpenStreetMap and are only requested after you press **Show the
map** in that browser, because they reveal the clip's location and your IP. In Wi-Fi detection
mode the radio stays on with a fixed MAC address, so the car can be recognised; Bluetooth
detection keeps Wi-Fi off away from home.

## Protect yourself

1. **Create a dedicated NAS account** with access to a single dedicated share. Never reuse a
   password that protects anything else. It will sit in clear text in a car.
2. **Put the Pi on its own Wi-Fi network**, isolated from the rest of your home, allowed to reach
   only port 445 of the NAS.
3. **Use Bluetooth detection** if you can, so that Wi-Fi is off whenever the car is away.
4. **Keep it updated** from the System page. Automatic updates are off because the root filesystem
   is read-only; the portal warns you after 30 days without a check.

If the Pi is lost or stolen, change the Wi-Fi and NAS passwords it knew.

## HTTPS (optional)

Put a certificate you trust in `/data/state/tls/cert.pem` and `/data/state/tls/key.pem` (owner root,
mode 600) and restart `teslapid`. The portal then serves HTTPS on port 443, redirects HTTP once
setup is finished and marks its cookie `Secure`.

```sh
mkcert teslapi.local   # for example, with https://github.com/FiloSottile/mkcert
```

If you reach the portal by a name other than `teslapi.local` or an IP address, allow it with
`-allow-host` in the service file, or requests will be rejected.
