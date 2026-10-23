#!/bin/sh
# setup.sh で作ったコンテナ、Windows 側の鍵と config、.work/ を消す。
set -eu

cd "$(dirname "$0")"
docker compose -p hashigo-poc -f ../e2e/compose.yaml -f compose.override.yaml down -v --remove-orphans
WIN_HOME=$(cd /mnt/c && cmd.exe /c 'echo %USERPROFILE%' 2>/dev/null | tr -d '\r')
rm -rf "$(wslpath -u "$WIN_HOME\\.ssh\\hashigo-poc")" .work
