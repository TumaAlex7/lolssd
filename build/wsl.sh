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
#
# Скачанное хранится в dist/cache: корневой архив Debian, пакеты apt, MSI. Пересборка ради
# правки в lolssd-wsl сети почти не трогает (GitHub бывает очень медленным). REFRESH=1 —
# скачать архив Debian и проверить версию MSI заново; пакеты apt обновляются всегда (apt
# сам докачивает только изменившиеся).
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

repo=$(cd "$(dirname "$0")/.." && pwd)
dist=$repo/dist
rel=${DEBIAN:-trixie}
w=$dist/wsl/build

for t in curl tar unshare cc; do
	command -v "$t" >/dev/null || die "нет $t"
done
grep -q "^$(id -un):" /etc/subuid || die "у пользователя нет диапазона в /etc/subuid (usermod --add-subuids)"
[[ -x $dist/wsl/lolssd-wsl ]] || die "сначала make dist"

cache=$dist/cache
mkdir -p "$cache"

echo "MSI WSL"
if [[ -z ${REFRESH:-} ]] && compgen -G "$dist/wsl/*.msi" >/dev/null; then
	echo "  $(basename "$(ls "$dist"/wsl/*.msi | head -1)") (из кэша; REFRESH=1 — проверить новую версию)"
else
	url=$(curl -fsSL --max-time 60 https://api.github.com/repos/microsoft/WSL/releases/latest |
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
fi

echo "Debian $rel"
# Каталог мог остаться от прошлой сборки с владельцами из subuid — удаляем его там же, где создали.
[[ -d $w ]] && unshare --map-auto --map-root-user rm -rf "$w"
mkdir -p "$w/root"
rootfs=$cache/debian-$rel-rootfs.tar.gz
if [[ -n ${REFRESH:-} || ! -s $rootfs ]]; then
	curl -fSL --retry 3 -o "$rootfs.part" \
		"https://raw.githubusercontent.com/debuerreotype/docker-debian-artifacts/dist-amd64/$rel/oci/blobs/rootfs.tar.gz"
	mv "$rootfs.part" "$rootfs"
else
	echo "  корневой архив из кэша ($(date -r "$rootfs" +%F); REFRESH=1 — скачать заново)"
fi
mkdir -p "$cache/apt-$rel"

unshare --map-auto --map-root-user --mount --pid --fork bash -euo pipefail -s "$w" "$repo" "$dist" "$rootfs" "$cache/apt-$rel" <<'NS'
w=$1 repo=$2 dist=$3 rootfs=$4 aptcache=$5 r=$w/root
tar -xzf "$rootfs" -C "$r"
mount -t proc proc "$r/proc"
# Только --rbind: вложенные точки /dev в userns «заперты» и переносятся лишь вместе с /dev.
# По той же причине снимается одним umount -l на корень, а не -R по одной.
mount --rbind /dev "$r/dev"
cp -L /etc/resolv.conf "$r/etc/resolv.conf"

# systemd — PID 1 в WSL (wsl.conf), с ним nspawn получает делегированные cgroups.
# dbus и libpam-systemd обязательны: с systemd WSL перед открытием терминала ждёт сеанс входа
# от systemd-logind, а logind без системной шины D-Bus его не создаёт («Timed out waiting
# for user session»), и терминал висит.
# Скачанные .deb — в кэше вне образа. Качает сам root (а не _apt из диапазона subuid), чтобы
# файлы кэша принадлежали пользователю. Перед apt-get clean кэш отключается — он не стирается.
# В Docker-образе Debian docker-clean стирает .deb сразу после установки — кэш остался бы пуст.
rm -f "$r/etc/apt/apt.conf.d/docker-clean"
mkdir -p "$r/var/cache/apt/archives"
mount --bind "$aptcache" "$r/var/cache/apt/archives"
apt="apt-get -o APT::Sandbox::User=root -o APT::Keep-Downloaded-Packages=true"
chroot "$r" $apt -q update >/dev/null
chroot "$r" env DEBIAN_FRONTEND=noninteractive $apt -qy install --no-install-recommends \
	systemd systemd-sysv systemd-container dbus libpam-systemd \
	cryptsetup lvm2 libxkbcommon-tools xkb-data ca-certificates kmod wayvnc >/dev/null
umount "$r/var/cache/apt/archives"
chroot "$r" apt-get clean
rm -rf "$r"/var/lib/apt/lists/*

install -m 0755 "$dist/wsl/lolssd-wsl" "$r/usr/local/bin/lolssd-wsl"
install -m 0644 "$repo/build/wsl/wsl.conf" "$r/etc/wsl.conf"
install -Dm 0644 "$repo/build/hypr/aquamarine-headless-device.patch" "$r/usr/local/share/lolssd/aquamarine-headless-device.patch"
install -Dm 0644 "$repo/build/hypr/gbm-probe.c" "$r/usr/local/share/lolssd/gbm-probe.c"
install -Dm 0644 "$repo/build/hypr/lolssd-gbm.c" "$r/usr/local/share/lolssd/lolssd-gbm.c"
# Модули под ядро WSL (make kmod), если собраны: их грузит lolssd-wsl перед запуском системы хоста.
if [[ -d $dist/wsl/modules ]]; then
	mkdir -p "$r/usr/local/lib/lolssd"
	cp -r "$dist/wsl/modules" "$r/usr/local/lib/lolssd/modules"
fi
# Только dlsym и strcmp из libc: собранное здесь работает и с glibc Debian.
mkdir -p "$r/usr/local/lib/lolssd"
cc -shared -fPIC -O2 -o "$r/usr/local/lib/lolssd/wayvnc-wlr-only.so" "$repo/build/hypr/wayvnc-wlr-only.c"
echo LANG=C.UTF-8 >"$r/etc/default/locale" # иначе pam_env ругается при каждом входе
: >"$r/etc/resolv.conf" # WSL создаёт свой при запуске

umount -l "$r/dev" "$r/proc"
# --one-file-system: даже если что-то осталось смонтированным, в архив попадёт только сама система.
tar --numeric-owner --one-file-system -czf "$dist/wsl/lolssd.tar" -C "$r" .
rm -rf "$w"
NS
echo "готово: $dist/wsl/lolssd.tar ($(du -h "$dist/wsl/lolssd.tar" | cut -f1))"
