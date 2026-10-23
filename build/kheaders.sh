#!/usr/bin/env bash
# lolssd: заголовки для сборки внешних модулей из собранного дерева ядра — как пакет linux-headers
# в Arch (PKGBUILD linux, _package-headers), только для x86.
#   build/kheaders.sh <дерево ядра> <куда>
set -euo pipefail

tree=$(realpath "$1")
out=$2
rm -rf "$out"
mkdir -p "$out"
cd "$tree"

install -Dt "$out" -m644 .config Makefile Module.symvers
cp -t "$out" -a localversion.* 2>/dev/null || true
install -Dt "$out/kernel" -m644 kernel/Makefile
install -Dt "$out/arch/x86" -m644 arch/x86/Makefile
cp -t "$out" -a scripts include
cp -t "$out/arch/x86" -a arch/x86/include
install -Dt "$out/arch/x86/kernel" -m644 arch/x86/kernel/asm-offsets.s
[[ -x tools/objtool/objtool ]] && install -Dt "$out/tools/objtool" tools/objtool/objtool
# Заголовки, которые внешние модули берут из каталогов драйверов (как у Arch).
install -Dt "$out/drivers/md" -m644 drivers/md/*.h
install -Dt "$out/net/mac80211" -m644 net/mac80211/*.h
# Kconfig и Makefile всех каталогов — Kbuild читает их при сборке модуля.
find . -path ./scripts -prune -o \( -name 'Kconfig*' -o -name 'Makefile*' -o -name 'Kbuild*' \) -type f -print |
	while read -r f; do install -Dm644 "$f" "$out/$f"; done
# Объектные файлы и прочее, что попало с scripts, модулям не нужно.
find "$out" -name '*.o' -delete -o -name '*.cmd' -delete -o -name '.*.d' -delete
