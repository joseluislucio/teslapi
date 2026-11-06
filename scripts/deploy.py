#!/usr/bin/env python3
"""Compila teslapid para la Pi (arm64), lo sube y aprovisiona o actualiza. Funciona en macOS, Linux
y Windows: solo necesita Go, Node y el cliente ssh.

    python3 scripts/deploy.py provision   # primera vez: particiones, imagen, sistema, solo lectura
    python3 scripts/deploy.py             # actualizar binario y ficheros del sistema
    python3 scripts/deploy.py --package paquete.tar.gz   # solo genera el paquete

Se configura con variables de entorno o con un fichero .deploy.env en la raíz del repo, que no se
sube (ver .deploy.env.example). Host: TESLAPI_HOST, por defecto <tu usuario>@teslapi.local.
TP_WIFI_*, TP_SMB_* y TP_CHARGER_* son opcionales y solo sirven para el primer config.json: lo que
falte lo pregunta el asistente del portal.
"""
import argparse
import getpass
import gzip
import io
import os
import shlex
import shutil
import subprocess
import sys
import tarfile
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
FIRST_RUN_VARS = ("TP_SMB_SHARE", "TP_SMB_USER", "TP_SMB_PASS", "TP_SMB_DOMAIN", "TP_WIFI_SSID", "TP_WIFI_PASS", "TP_CHARGER_ADDR", "TP_CHARGER_NAME")
REMOTE = "/tmp/teslapi-deploy"


def load_env_file(path, env):
    """KEY=valor por línea. Lo que ya venga del entorno tiene prioridad."""
    if not path.exists():
        return
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, val = line.split("=", 1)
        key = key.replace("export ", "").strip()
        val = val.strip()
        if len(val) >= 2 and val[0] == val[-1] and val[0] in "\"'":
            val = val[1:-1]
        env.setdefault(key, val)


def tool(name):
    # En Windows npm es npm.cmd: hay que dar la ruta completa para lanzarlo sin shell.
    path = shutil.which(name)
    if not path:
        sys.exit(f"falta {name}: instálalo y vuelve a probar")
    return path


def file_mode(rel):
    return 0o755 if rel.endswith(".sh") or rel == "teslapid" else 0o644


def normalize(data):
    """Un checkout de Windows puede traer CRLF, y un script con CRLF no arranca en la Pi."""
    if b"\0" in data[:8192]:
        return data
    return data.replace(b"\r\n", b"\n")


def add_file(tar, rel, data):
    info = tarfile.TarInfo("./" + rel)
    info.size = len(data)
    info.mode = file_mode(rel)
    info.mtime = 0
    tar.addfile(info, io.BytesIO(data))


def add_dir(tar, rel):
    info = tarfile.TarInfo("./" + rel)
    info.type = tarfile.DIRTYPE
    info.mode = 0o755
    info.mtime = 0
    tar.addfile(info)


def package(binary):
    """Los permisos se fijan aquí y no se leen del disco, porque Windows no los conserva."""
    buf = io.BytesIO()
    with gzip.GzipFile(fileobj=buf, mode="wb", mtime=0) as gz, tarfile.open(fileobj=gz, mode="w") as tar:
        add_file(tar, "teslapid", binary)
        for top in ("system", "setup"):
            add_dir(tar, top)
            for path in sorted((REPO / top).rglob("*")):
                rel = path.relative_to(REPO).as_posix()
                if path.name.startswith("._") or path.name == ".DS_Store":
                    continue
                if path.is_dir():
                    add_dir(tar, rel)
                else:
                    add_file(tar, rel, normalize(path.read_bytes()))
    return buf.getvalue()


def version():
    git = shutil.which("git")
    if not git:
        return "dev"
    r = subprocess.run([git, "describe", "--always", "--dirty"], cwd=REPO, capture_output=True, text=True)
    return r.stdout.strip() if r.returncode == 0 and r.stdout.strip() else "dev"


def build():
    subprocess.run([tool("npm"), "run", "build", "--silent"], cwd=REPO / "web", check=True)
    ver = version()
    print(f"==> compilando teslapid {ver} (linux/arm64)", flush=True)
    out = REPO / "build" / "teslapid"
    out.parent.mkdir(exist_ok=True)
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "arm64"}
    subprocess.run([tool("go"), "build", "-trimpath", "-ldflags", f"-s -w -X main.version={ver}", "-o", str(out), "./cmd/teslapid"], cwd=REPO / "daemon", env=env, check=True)
    return out.read_bytes()


def first_run_script(env):
    # Variables solo para el primer config.json; viajan por la sesión SSH, no por argv.
    exports = " ".join(f"{k}={shlex.quote(env.get(k, ''))}" for k in FIRST_RUN_VARS)
    return f"export {exports}\nsudo -E {REMOTE}/setup/provision.sh all\n"


def main():
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    p = argparse.ArgumentParser(description="Compila y despliega teslapi en la Pi")
    p.add_argument("mode", nargs="?", default="install", choices=("install", "provision"))
    p.add_argument("--package", metavar="FICHERO", help="solo genera el paquete, sin conectarse a la Pi")
    args = p.parse_args()

    env = dict(os.environ)
    load_env_file(REPO / ".deploy.env", env)
    data = package(build())
    if args.package:
        Path(args.package).write_bytes(data)
        print(f"Paquete: {args.package} ({len(data) / 1e6:.1f} MB)")
        return

    ssh = [tool("ssh"), "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15"]
    host = env.get("TESLAPI_HOST") or f"{getpass.getuser().lower()}@teslapi.local"
    print(f"==> subiendo a {host}", flush=True)
    try:
        subprocess.run([*ssh, host, f"rm -rf {REMOTE} && mkdir -p {REMOTE} && tar -xzf - -C {REMOTE}"], input=data, check=True)
    except subprocess.CalledProcessError:
        sys.exit(f"No puedo conectar con {host}. Comprueba que la Pi está encendida o pon TESLAPI_HOST=usuario@IP en .deploy.env.")
    if args.mode == "provision":
        subprocess.run([*ssh, host, "bash -s"], input=first_run_script(env).encode(), check=True)
    else:
        subprocess.run([*ssh, host, f"sudo {REMOTE}/setup/provision.sh install && rm -rf {REMOTE}; systemctl --no-pager status teslapid | head -5"], check=True)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        sys.exit(f"Ha fallado un paso ({e.returncode}): {' '.join(map(str, e.cmd[:3]))}…")
