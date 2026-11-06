# Changelog

## Unreleased

Security hardening before the first public release.

### Changed

- A portal password is now required to finish setup, with at least 8 characters, and cannot be
  removed. Existing installations without one go back to the setup wizard.
- The setup network is always WPA2 with a per-device password, and turns off after 15 idle minutes.
- Logging out closes every session. Sessions last 14 days instead of 30.
- A stored password is only reused for the same NAS server and user, or the same Wi-Fi network;
  after changing them you have to type it again.
- With an address configured, a Bluetooth device only matches by address, not by name.
- The map only loads after you ask for it, because it tells OpenStreetMap where the clip was.
- Before deleting clips from the drive, they are compared byte by byte with the NAS copy.

### Added

- The install scripts now run on macOS, Linux and Windows (`scripts/flash.py`, `scripts/deploy.py`).
- MIT licence, and third-party notices in the repository and in release packages.
- Optional HTTPS, optional SMB3 encryption, firewall, SSH hardening and service sandboxing.
- `Host`, `Origin` and `Content-Type` checks, Content-Security-Policy and other security headers.
- Login back-off per IP, stream limit and server timeouts.

### Fixed

- Path traversal through the NAS folder, which allowed reading any file as root.
- Option injection into `mount` through the SMB version.
- Script execution through `event.json` and through files served from the drive or the NAS.
- Password masking failed for passwords containing "pass" or "psk".
- The Wi-Fi password stayed on the boot partition and in cloud-init copies after provisioning.
- A file of the same size on the NAS counted as archived without being copied.
- Go updated to 1.27.3 (24 reachable vulnerabilities in the previous standard library).
