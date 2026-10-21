//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lolssd/hostprofile"
)

// Хук pacman, который прерывает любую транзакцию с ядром или /boot. ESP хоста внутри Windows
// не смонтирован: обновлённые модули в /usr/lib/modules при старом ядре на ESP сломали бы
// загрузку хоста.
const pacmanHook = `[Trigger]
Operation = Install
Operation = Upgrade
Operation = Remove
Type = Path
Target = usr/lib/modules/*
Target = boot/*

[Action]
Description = lolssd: обновление ядра и /boot внутри Windows запрещено (ESP хоста не смонтирован)
When = PreTransaction
Exec = /usr/bin/false
AbortOnFail
`

// Внутри контейнера всё WSL-шное лежит под /run (tmpfs nspawn), чтобы точки монтирования
// не создавались на диске хоста.
const inner = "/run/lolssd-wsl"

// run запускает команду в системе хоста от имени пользователя профиля.
func run(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		usage()
	}
	if code := up(); code != 0 {
		return code
	}
	p, err := loadProfile()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return runIn(p, nil, args)
}

// shell — оболочка пользователя в системе хоста, в окне терминала Windows (ярлык «Linux shell»).
func shell() int {
	if code := up(); code != 0 {
		return code
	}
	p, err := loadProfile()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sh := p.User.Shell
	if sh == "" {
		sh = "/bin/sh"
	}
	return runIn(p, nil, []string{sh, "-l"})
}

// runIn — systemd-nspawn без --boot. Графика и звук идут через WSLg, ускорение — через
// GPU-PV (/dev/dxg + d3d12 в Mesa хоста). На диск хоста nspawn ничего не пишет: журнал,
// часовой пояс и resolv.conf не копируются. extra — дополнительные аргументы nspawn.
func runIn(p hostprofile.Profile, extra, args []string) int {
	nspawn := []string{
		"--quiet", "--as-pid2", "--register=no",
		"--link-journal=no", "--timezone=off", "--resolv-conf=bind-host",
		"-D", rootDir, "--user=" + p.User.Name, "--chdir=" + p.User.Home,
		"--bind=/mnt/wslg:" + inner + "/wslg",
		"--bind-ro=/usr/lib/wsl:" + inner + "/lib",
		// /tmp хоста — каталог на его диске (в памяти он только при обычной загрузке хоста).
		"--tmpfs=/tmp",
		"--bind=/mnt/wslg/.X11-unix:/tmp/.X11-unix",
		"--setenv=XDG_RUNTIME_DIR=" + inner + "/wslg/runtime-dir",
		"--setenv=WAYLAND_DISPLAY=wayland-0",
		"--setenv=DISPLAY=:0",
		"--setenv=PULSE_SERVER=unix:" + inner + "/wslg/PulseServer",
		"--setenv=GALLIUM_DRIVER=d3d12",
		"--setenv=LD_LIBRARY_PATH=" + inner + "/lib/lib",
		"--setenv=LOLSSD=wsl",
	}
	// Без GPU-PV (старый драйвер видеокарты, выключенный WSLg) работаем на llvmpipe.
	if _, err := os.Stat("/dev/dxg"); err == nil {
		nspawn = append(nspawn, "--bind=/dev/dxg", "--property=DeviceAllow=/dev/dxg rw")
	} else {
		fmt.Fprintln(os.Stderr, "внимание: нет /dev/dxg, графика без ускорения")
	}
	if p.Distro.ID == "arch" || strings.Contains(p.Distro.IDLike, "arch") {
		upper := "/run/lolssd/pacman.d"
		err := os.MkdirAll(filepath.Join(upper, "hooks"), 0o755)
		if err == nil {
			err = os.WriteFile(filepath.Join(upper, "hooks", "zz-lolssd-block-kernel.hook"), []byte(pacmanHook), 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "хук pacman:", err)
			return 1
		}
		// overlay поверх /etc/pacman.d хоста: хук виден pacman, а на диск хоста не попадает.
		nspawn = append(nspawn, "--overlay=+/etc/pacman.d:"+upper+":/etc/pacman.d")
	} else {
		fmt.Fprintln(os.Stderr, "внимание: для", p.Distro.ID, "обновления ядра изнутри Windows не блокируются — не обновляйте систему отсюда")
	}
	nspawn = append(append(append(nspawn, extra...), "--"), args...)

	cmd := exec.Command("systemd-nspawn", nspawn...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "systemd-nspawn:", err)
		return 1
	}
	return 0
}
