#!/usr/bin/env bash
# lolssd: почистить образ Windows как tiny11 — настоящим DISM, в WinPE внутри QEMU.
#   build/tiny.sh <распакованный ISO> <install.wim с одной редакцией> <lolssd.reg> <результат.wim>
# Вызывается из build/iso.sh. Root не нужен (KVM доступен пользователю).
#
# 1. Собирается маленький загрузочный ISO: загрузчик и boot.wim из исходного ISO, где в WinPE
#    (образ 1) подменён startnet.cmd, плюс полезная нагрузка — образ, разрезанный на .swm,
#    build/tiny/tiny.cmd, списки и lolssd.reg с путями под загруженные кусты реестра.
# 2. QEMU без окна грузит его с пустым рабочим диском; tiny.cmd делает всё DISM'ом и reg
#    (см. сам скрипт) и выключает машину.
# 3. С рабочего диска забираются clean.wim и журнал.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

tree=$1 wim=$2 reg=$3 out=$4
repo=$(cd "$(dirname "$0")/.." && pwd)
code=/usr/share/edk2/x64/OVMF_CODE.4m.fd vars=/usr/share/edk2/x64/OVMF_VARS.4m.fd
for t in qemu-system-x86_64 wimlib-imagex xorriso 7z sfdisk; do
	command -v "$t" >/dev/null || die "нет $t"
done
[[ -f $code ]] || die "нет OVMF ($code), sudo pacman -S edk2-ovmf"

w=$(dirname "$out")/tiny
rm -rf "$w"
mkdir -p "$w/payload"
crlf() { sed 's/\r*$/\r/' "$1" >"$2"; }

wimlib-imagex split "$wim" "$w/payload/install.swm" 3800 >/dev/null
crlf "$repo/build/tiny/tiny.cmd" "$w/payload/tiny.cmd"
crlf "$repo/build/tiny/keep-apps.txt" "$w/payload/keep-apps.txt"
crlf "$repo/build/tiny/remove-packages.txt" "$w/payload/remove-packages.txt"
# Кусты образа загружаются как HKLM\zSYSTEM и HKLM\zSOFTWARE; CurrentControlSet в офлайн-кусте
# не существует, есть ControlSet001.
sed -e 's/^\[HKEY_LOCAL_MACHINE\\SYSTEM\\CurrentControlSet\\/[HKEY_LOCAL_MACHINE\\zSYSTEM\\ControlSet001\\/' \
	-e 's/^\[HKEY_LOCAL_MACHINE\\SOFTWARE\\/[HKEY_LOCAL_MACHINE\\zSOFTWARE\\/' "$reg" | sed 's/\r*$/\r/' >"$w/payload/lolssd-offline.reg"
echo lolssd >"$w/payload/lolssd-tiny.txt"

# WinPE: образ 1 boot.wim, загружается он, а не установщик (образ 2).
cp "$tree/sources/boot.wim" "$w/boot.wim"
chmod u+w "$w/boot.wim"
crlf "$repo/build/tiny/startnet.cmd" "$w/startnet.cmd"
# --rebuild: загрузчик Windows не грузит WIM, в который wimlib дописал данные в конец
# (0xc00000bb на winload.efi); переписанный целиком файл он принимает.
echo "add '$w/startnet.cmd' '/Windows/System32/startnet.cmd'" | wimlib-imagex update "$w/boot.wim" 1 --rebuild >/dev/null
wimlib-imagex info "$w/boot.wim" --boot 1 >/dev/null

# Два диска: загрузочный — только WinPE, полезная нагрузка — отдельно. Если положить части образа
# (по 3,8 ГБ) рядом, boot.wim уезжает за 4 ГБ от начала диска, и загрузчик Windows его не читает.
# boot/ нужен: там boot.sdi, RAM-диск, на котором работает WinPE (без него 0xc000000f).
xorriso -as mkisofs -quiet -iso-level 3 -J -joliet-long -V LOLSSD_PE \
	-e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot -graft-points -o "$w/pe.iso" \
	efi="$tree/efi" boot="$tree/boot" bootmgr.efi="$tree/bootmgr.efi" sources/boot.wim="$w/boot.wim"
xorriso -as mkisofs -quiet -iso-level 3 -J -joliet-long -V LOLSSD_TINY -o "$w/payload.iso" "$w/payload"
rm -rf "$w/payload"

# Рабочий диск: образ, смонтированный DISM'ом, раскрывается в ~25 ГБ файлов.
truncate -s 64G "$w/work.img"
cp "$vars" "$w/vars.fd"
echo "  чистка в виртуальной машине (DISM), 15–30 минут; окна не будет"
# TINY_QEMU — дополнительные аргументы для отладки, например монитор для снимков экрана:
#   TINY_QEMU="-monitor unix:/tmp/tiny.mon,server,nowait"
# shellcheck disable=SC2086
qemu-system-x86_64 -enable-kvm -machine q35 -cpu host -smp 4 -m 4G -display none -nic none ${TINY_QEMU:-} \
	-drive if=pflash,format=raw,readonly=on,file="$code" -drive if=pflash,format=raw,file="$w/vars.fd" \
	-device ahci,id=ahci \
	-drive if=none,id=work,format=raw,cache=unsafe,file="$w/work.img" -device ide-hd,drive=work,bus=ahci.0 \
	-drive if=none,id=pe,media=cdrom,readonly=on,file="$w/pe.iso" -device ide-cd,drive=pe,bus=ahci.1,bootindex=0 \
	-drive if=none,id=payload,media=cdrom,readonly=on,file="$w/payload.iso" -device ide-cd,drive=payload,bus=ahci.2

# Раздел NTFS начинается там, где его создал diskpart; копия без нулей занимает только данные.
start=$(sfdisk -d "$w/work.img" 2>/dev/null | sed -n 's/.*start= *\([0-9]*\).*/\1/p' | head -1 || true)
[[ -n $start ]] || die "виртуальная машина не разметила рабочий диск: WinPE не дошёл до tiny.cmd"
dd if="$w/work.img" of="$w/work.ntfs" bs=4M iflag=skip_bytes skip=$((start * 512)) conv=sparse status=none
rm "$w/work.img"
7z e -y "$w/work.ntfs" -o"$w" tiny.log done.txt clean.wim >/dev/null 2>&1 || true
rm "$w/work.ntfs"
cp "$w/tiny.log" "$(dirname "$out")/tiny.log" 2>/dev/null || true
grep -E '^(ok|FAIL)|FAILED|Error' "$w/tiny.log" 2>/dev/null | sed 's/^/    /' | tail -20 || true
[[ -f $w/done.txt && -f $w/clean.wim ]] || die "чистка не завершилась, журнал: $(dirname "$out")/tiny.log"
mv "$w/clean.wim" "$out"
rm -rf "$w"
