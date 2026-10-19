# Пусковой WSL-дистрибутив lolssd: минимальный Arch, из которого поднимается система хоста.
# Собирается make wsl, экспортируется в tar для wsl --import.
FROM archlinux:base

# systemd-nspawn (входит в base), LUKS и LVM хоста, сборка раскладки для Hyprland.
RUN pacman -Syu --noconfirm --needed cryptsetup lvm2 libxkbcommon xkeyboard-config \
 && pacman -Scc --noconfirm \
 && rm -rf /var/lib/pacman/sync/*

COPY wsl.conf /etc/wsl.conf
COPY lolssd-wsl /usr/local/bin/lolssd-wsl
