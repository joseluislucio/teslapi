#!/bin/bash
# Compila teslapid para la Pi (arm64), lo sube y aprovisiona o actualiza.
#
#   scripts/deploy.sh provision   # primera vez: particiones, imagen, sistema, solo lectura
#   scripts/deploy.sh             # actualizar binario y ficheros del sistema
#
# Se configura con variables de entorno o con un fichero .deploy.env en la raíz del repo, que
# no se sube (ver .deploy.env.example). Host: TESLAPI_HOST, por defecto <tu usuario>@teslapi.local.
# TP_WIFI_*, TP_SMB_* y TP_CHARGER_* son opcionales y solo sirven para el primer config.json:
# lo que falte lo pregunta el asistente del portal.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -f .deploy.env ]; then
	# shellcheck disable=SC1091
	. ./.deploy.env
fi
HOST=${TESLAPI_HOST:-$(whoami)@teslapi.local}
MODE=${1:-install}
VERSION=$(git describe --always --dirty 2>/dev/null || echo dev)

make -s web
echo "==> compilando teslapid $VERSION (linux/arm64)"
(cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o ../build/teslapid ./cmd/teslapid)

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT
cp build/teslapid "$STAGE/"
cp -R system setup "$STAGE/"
COPYFILE_DISABLE=1 tar -C "$STAGE" -czf - . | ssh "$HOST" 'rm -rf /tmp/teslapi-deploy && mkdir -p /tmp/teslapi-deploy && tar -xzf - -C /tmp/teslapi-deploy'

if [ "$MODE" = provision ]; then
	# Variables solo para el primer config.json; viajan por la sesión SSH, no por argv.
	{
		printf 'export TP_SMB_SHARE=%q TP_SMB_USER=%q TP_SMB_PASS=%q TP_SMB_DOMAIN=%q\n' \
			"${TP_SMB_SHARE:-}" "${TP_SMB_USER:-}" "${TP_SMB_PASS:-}" "${TP_SMB_DOMAIN:-}"
		printf 'export TP_WIFI_SSID=%q TP_WIFI_PASS=%q\n' "${TP_WIFI_SSID:-}" "${TP_WIFI_PASS:-}"
		printf 'export TP_CHARGER_ADDR=%q TP_CHARGER_NAME=%q\n' "${TP_CHARGER_ADDR:-}" "${TP_CHARGER_NAME:-}"
		echo 'sudo -E /tmp/teslapi-deploy/setup/provision.sh all'
	} | ssh -t "$HOST" bash -s
else
	ssh "$HOST" 'sudo /tmp/teslapi-deploy/setup/provision.sh install; systemctl --no-pager status teslapid | head -5'
fi
