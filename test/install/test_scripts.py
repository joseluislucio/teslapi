#!/usr/bin/env python3
"""Pruebas de flash.py y deploy.py que no tocan ningún disco. Corren en los tres sistemas."""
import gzip
import io
import json
import struct
import sys
import tarfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent.parent / "scripts"))
import deploy  # noqa: E402
import flash  # noqa: E402


def mbr(p2_start=532480):
    b = bytearray(512)
    b[510:512] = b"\x55\xaa"
    b[446 + 16 + 8 : 446 + 16 + 12] = struct.pack("<I", p2_start)
    return bytes(b)


class Mbr(unittest.TestCase):
    def test_third_partition_is_aligned_and_fills_the_card(self):
        size = 128 * 10**9
        b, start, total = flash.patch_mbr(mbr(), size, 6)
        self.assertEqual(start % 8192, 0)
        self.assertGreaterEqual(start, 532480 + 6 * 1024 * 1024 * 2)
        self.assertEqual(total, size // 512)
        e = b[446 + 32 : 446 + 48]
        self.assertEqual(e[4], 0x83)
        self.assertEqual(struct.unpack("<II", e[8:16]), (start, total - start))
        self.assertEqual(b[510:512], b"\x55\xaa")
        self.assertEqual(b[:446], mbr()[:446])

    def test_rejects_invalid_mbr_and_small_card(self):
        with self.assertRaises(ValueError):
            flash.patch_mbr(bytes(512), 128 * 10**9, 6)
        with self.assertRaises(ValueError):
            flash.patch_mbr(mbr(), 4 * 10**9, 6)


class BootFiles(unittest.TestCase):
    def test_cmdline_drops_resize_and_sets_country(self):
        got = flash.edit_cmdline("console=tty1 root=PARTUUID=x resize quiet\r\n", "ES")
        self.assertEqual(got, "console=tty1 root=PARTUUID=x quiet cfg80211.ieee80211_regdom=ES\n")

    def test_network_config_keeps_awkward_values_intact(self):
        ssid, psk = 'Mi "Casa" & #1: sí', "pa\"ss\\w&rd#'"
        ap = json.loads(flash.network_config(ssid, psk, "ES"))["network"]["wifis"]["wlan0"]
        self.assertEqual(ap["access-points"][ssid]["password"], psk)
        self.assertEqual(ap["regulatory-domain"], "ES")

    def test_open_network_has_no_password(self):
        ap = json.loads(flash.network_config("abierta", "", "ES"))["network"]["wifis"]["wlan0"]
        self.assertEqual(ap["access-points"]["abierta"], {})

    def test_teslapi_txt_fills_only_its_keys(self):
        tpl = "# wifi_ssid = ejemplo\r\nwifi_ssid =\nwifi_password =\nsetup_ap_password =\nsmb_user =\n"
        got = flash.teslapi_txt(tpl, r"a\1&b", r"c\gd", "APPASS")
        self.assertEqual(got, "# wifi_ssid = ejemplo\n" + "wifi_ssid = a\\1&b\nwifi_password = c\\gd\nsetup_ap_password = APPASS\nsmb_user =\n")

    def test_user_data_quotes_the_key_comment(self):
        got = flash.user_data("ana", "Europe/Madrid", "ssh-ed25519 AAAA pc\\ana: #1")
        self.assertIn('      - "ssh-ed25519 AAAA pc\\\\ana: #1"\n', got)
        self.assertIn("  - name: ana\n", got)
        self.assertNotIn("\r", got)

    def test_usernames(self):
        for ok in ("pepo", "ana-1", "_x"):
            self.assertTrue(flash.valid_user(ok), ok)
        for bad in ("Ana", "ana maria", "1ana", "", "a\nb", "a:b"):
            self.assertFalse(flash.valid_user(bad), bad)

    def test_setup_network_password(self):
        a, b = flash.ap_password(), flash.ap_password()
        self.assertEqual(len(a), 16)
        self.assertNotEqual(a, b)
        self.assertFalse(set(a) & set("lIO01o"))


class DiskChoice(unittest.TestCase):
    """Lo más delicado: un fallo aquí borra el disco equivocado."""

    def test_mac(self):
        self.assertIsNone(flash.mac_refusal({"Internal": False}))
        self.assertIsNone(flash.mac_refusal({"Internal": True, "Removable": True, "BusProtocol": "Secure Digital"}))
        self.assertTrue(flash.mac_refusal({"Internal": True, "Removable": False, "BusProtocol": "Apple Fabric"}))
        self.assertTrue(flash.mac_refusal({"Internal": True, "Removable": True, "BusProtocol": "USB"}))

    def test_linux(self):
        sd = {"name": "sdb", "type": "disk", "rm": True, "ro": False, "tran": "usb"}
        self.assertIsNone(flash.linux_refusal(sd, []))
        self.assertIsNone(flash.linux_refusal({"name": "mmcblk0", "type": "disk", "rm": False, "ro": False}, ["/media/x"]))
        self.assertTrue(flash.linux_refusal({"name": "sda", "type": "disk", "rm": False, "ro": False, "tran": "sata"}, []))
        self.assertTrue(flash.linux_refusal({"name": "nvme0n1", "type": "disk", "rm": False, "ro": False, "tran": "nvme"}, []))
        self.assertTrue(flash.linux_refusal(sd, ["/"]))
        self.assertTrue(flash.linux_refusal(sd, ["/boot/efi"]))
        self.assertTrue(flash.linux_refusal({**sd, "ro": True}, []))
        self.assertTrue(flash.linux_refusal({"name": "loop0", "type": "loop", "rm": False, "ro": False}, []))

    def test_windows(self):
        usb = {"BusType": "USB", "IsBoot": False, "IsSystem": False, "IsReadOnly": False}
        self.assertIsNone(flash.windows_refusal(usb))
        self.assertIsNone(flash.windows_refusal({**usb, "BusType": "SD"}))
        self.assertTrue(flash.windows_refusal({**usb, "BusType": "NVMe"}))
        self.assertTrue(flash.windows_refusal({**usb, "BusType": "SATA"}))
        self.assertTrue(flash.windows_refusal({**usb, "BusType": "File Backed Virtual"}))
        self.assertTrue(flash.windows_refusal({**usb, "IsBoot": True}))
        self.assertTrue(flash.windows_refusal({**usb, "IsSystem": True}))
        self.assertTrue(flash.windows_refusal({**usb, "IsReadOnly": True}))

    def test_test_override_can_never_reach_the_system_disk(self):
        self.assertTrue(flash.hard_refusal({"IsBoot": True}))
        self.assertTrue(flash.hard_refusal({"IsSystem": True}))
        self.assertTrue(flash.hard_refusal({"mounts": ["/"]}))
        self.assertTrue(flash.hard_refusal({"mounts": ["/boot/efi"]}))
        self.assertFalse(flash.hard_refusal({"BusType": "File Backed Virtual", "IsBoot": False, "IsSystem": False}))

    def test_device_names(self):
        self.assertEqual(flash.Mac().parse("/dev/disk4"), "/dev/disk4")
        self.assertIsNone(flash.Mac().parse("/dev/disk4s1"))
        self.assertIsNone(flash.Mac().parse("/dev/disk4; rm -rf /"))
        self.assertEqual(flash.Linux().parse("/dev/sdb"), "/dev/sdb")
        self.assertEqual(flash.Linux().parse("/dev/mmcblk0"), "/dev/mmcblk0")
        self.assertIsNone(flash.Linux().parse("/dev/sdb1"))
        self.assertIsNone(flash.Linux().parse("/dev/nvme0n1"))
        self.assertEqual(flash.Windows().parse("3"), "3")
        self.assertEqual(flash.Windows().parse(r"\\.\PhysicalDrive3"), "3")
        self.assertIsNone(flash.Windows().parse("C:"))

    def test_linux_partition_names(self):
        self.assertEqual(flash.Linux()._part("/dev/sdb", 1), "/dev/sdb1")
        self.assertEqual(flash.Linux()._part("/dev/mmcblk0", 1), "/dev/mmcblk0p1")
        self.assertEqual(flash.Linux()._part("/dev/loop3", 1), "/dev/loop3p1")


class Deploy(unittest.TestCase):
    def test_package_has_lf_scripts_and_the_right_modes(self):
        with tarfile.open(fileobj=io.BytesIO(gzip.decompress(deploy.package(b"binario"))), mode="r") as t:
            names = {m.name: m for m in t.getmembers()}
            self.assertEqual(names["./teslapid"].mode, 0o755)
            self.assertEqual(names["./setup/provision.sh"].mode, 0o755)
            self.assertEqual(names["./system/teslapi-gadget.sh"].mode, 0o755)
            self.assertEqual(names["./system/boot/teslapi.txt"].mode, 0o644)
            self.assertTrue(names["./system"].isdir())
            for name, m in names.items():
                self.assertNotIn("\\", name)
                if m.isfile() and name != "./teslapid":
                    self.assertNotIn(b"\r\n", t.extractfile(m).read(), name)

    def test_normalize_leaves_binaries_alone(self):
        self.assertEqual(deploy.normalize(b"a\r\nb\n"), b"a\nb\n")
        self.assertEqual(deploy.normalize(b"\0\r\n"), b"\0\r\n")

    def test_env_file_does_not_override_environment(self):
        import tempfile

        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "env"
            f.write_text("# nota\nexport A=1\nB='dos dos'\nC=3\nmal\n", encoding="utf-8")
            env = {"C": "entorno"}
            deploy.load_env_file(f, env)
        self.assertEqual(env, {"A": "1", "B": "dos dos", "C": "entorno"})

    def test_first_run_script_quotes_secrets(self):
        s = deploy.first_run_script({"TP_WIFI_PASS": "a'b $(rm) c"})
        self.assertIn("""TP_WIFI_PASS='a'"'"'b $(rm) c'""", s)
        self.assertTrue(s.endswith("sudo -E /tmp/teslapi-deploy/setup/provision.sh all\n"))


if __name__ == "__main__":
    unittest.main()
