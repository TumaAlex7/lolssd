#!/bin/sh
# Запуск lolssd с раздела LOLSSD: sh /путь/к/LOLSSD/lolssd.sh check
# udisks монтирует FAT с showexec, и запустить бинарник прямо с раздела нельзя. Поэтому
# запускается его копия из /tmp, а LOLSSD_ROOT говорит ей, откуда она на самом деле.
set -e
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
cp "$here/host/lolssd" "$tmp/lolssd"
chmod +x "$tmp/lolssd"
LOLSSD_ROOT=$here exec "$tmp/lolssd" "$@"
