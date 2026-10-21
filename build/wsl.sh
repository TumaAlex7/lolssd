#!/usr/bin/env bash
# lolssd: собрать пусковой WSL-дистрибутив и скачать MSI WSL. Без Docker и без root, на любом
# Linux с util-linux ≥ 2.38 (unshare --map-auto) и настроенными /etc/subuid, /etc/subgid.
#   build/wsl.sh            (обычно make wsl)
#
# Пусковой дистрибутив — Debian stable (DEBIAN=trixie): это «прибор» для systemd-nspawn,
# cryptsetup, lvm2, xkbcli и wayvnc (рабочий стол, cmd/lolssd-wsl/desktop.go), он не должен
# протухать, как rolling release. Основа — официальный корневой архив Debian, из которого
# собираются их Docker-образы (debuerreotype). Пакеты ставит apt самого Debian внутри
# пространства имён пользователя: там мы root, а файлы получают настоящих владельцев из
# диапазона subuid.
#
# Результат: dist/wsl/lolssd.tar (для wsl --import) и dist/wsl/wsl.x64.msi.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

repo=$(cd "$(dirname "$0")/.." && pwd)
dist=$repo/dist
rel=${DEBIAN:-trixie}
w=$dist/wsl/build

for t in curl tar unshare; do
	command -v "$t" >/dev/null || die "нет $t"
done
grep -q "^$(id -un):" /etc/subuid || die "у пользователя нет диапазона в /etc/subuid (usermod --add-subuids)"
[[ -x $dist/wsl/lolssd-wsl ]] || die "сначала make dist"

echo "MSI WSL"
url=$(curl -fsSL https://api.github.com/repos/microsoft/WSL/releases/latest |
	grep -o '"browser_download_url": *"[^"]*x64\.msi"' | head -1 | sed 's/.*"\(https[^"]*\)"/\1/')
[[ -n $url ]] || die "не нашёл MSI в последнем выпуске microsoft/WSL"
# Та же версия уже скачана — не качаем 370 МБ заново. Недокачанное продолжаем с места обрыва
# (-C -). Медленнее 50 КБ/с минуту — обрываем, но сборку дистрибутива не прерываем: MSI нужен
# только для make iso, а GitHub бывает очень медленным.
msi=$dist/wsl/$(basename "$url")
if [[ ! -s $msi ]]; then
	if curl -fSL -C - --retry 3 --speed-limit 50000 --speed-time 60 -o "$msi.part" "$url"; then
		mv "$msi.part" "$msi"
	else
		echo "  внимание: MSI не докачан ($(du -h "$msi.part" | cut -f1) из $url); make wsl докачает его в следующий раз" >&2
	fi
fi
if [[ -s $msi ]]; then
	find "$dist/wsl" -maxdepth 1 -name '*.msi' ! -name "$(basename "$msi")" -delete
	echo "  $(basename "$msi")"
fi

echo "Debian $rel"
# Каталог мог остаться от прошлой сборки с владельцами из subuid — удаляем его там же, где создали.
[[ -d $w ]] && unshare --map-auto --map-root-user rm -rf "$w"
mkdir -p "$w/root"
curl -fsSL -o "$w/rootfs.tar.gz" \
	"https://raw.githubusercontent.com/debuerreotype/docker-debian-artifacts/dist-amd64/$rel/oci/blobs/rootfs.tar.gz"

unshare --map-auto --map-root-user --mount --pid --fork bash -euo pipefail -s "$w" "$repo" "$dist" <<'NS'
w=$1 repo=$2 dist=$3 r=$w/root
tar -xzf "$w/rootfs.tar.gz" -C "$r"
mount -t proc proc "$r/proc"
# Только --rbind: вложенные точки /dev в userns «заперты» и переносятся лишь вместе с /dev.
# По той же причине снимается одним umount -l на корень, а не -R по одной.
mount --rbind /dev "$r/dev"
cp -L /etc/resolv.conf "$r/etc/resolv.conf"

# systemd — PID 1 в WSL (wsl.conf), с ним nspawn получает делегированные cgroups.
# dbus и libpam-systemd обязательны: с systemd WSL перед открытием терминала ждёт сеанс входа
# от systemd-logind, а logind без системной шины D-Bus его не создаёт («Timed out waiting
# for user session»), и терминал висит.
chroot "$r" apt-get -q update >/dev/null
chroot "$r" env DEBIAN_FRONTEND=noninteractive apt-get -qy install --no-install-recommends \
	systemd systemd-sysv systemd-container dbus libpam-systemd \
	cryptsetup lvm2 libxkbcommon-tools xkb-data ca-certificates kmod wayvnc >/dev/null
chroot "$r" apt-get clean
rm -rf "$r"/var/lib/apt/lists/*

install -m 0755 "$dist/wsl/lolssd-wsl" "$r/usr/local/bin/lolssd-wsl"
install -m 0644 "$repo/build/wsl/wsl.conf" "$r/etc/wsl.conf"
install -Dm 0644 "$repo/build/hypr/aquamarine-headless-device.patch" "$r/usr/local/share/lolssd/aquamarine-headless-device.patch"
install -Dm 0644 "$repo/build/hypr/gbm-probe.c" "$r/usr/local/share/lolssd/gbm-probe.c"
echo LANG=C.UTF-8 >"$r/etc/default/locale" # иначе pam_env ругается при каждом входе
: >"$r/etc/resolv.conf" # WSL создаёт свой при запуске

umount -l "$r/dev" "$r/proc"
# --one-file-system: даже если что-то осталось смонтированным, в архив попадёт только сама система.
tar --numeric-owner --one-file-system -czf "$dist/wsl/lolssd.tar" -C "$r" .
rm -rf "$w"
NS
echo "готово: $dist/wsl/lolssd.tar ($(du -h "$dist/wsl/lolssd.tar" | cut -f1))"
