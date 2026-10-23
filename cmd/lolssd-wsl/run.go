//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
const (
	inner     = "/run/lolssd-wsl"
	sharedTmp = "/run/lolssd/tmp" // /tmp контейнеров, в памяти пускового дистрибутива
)

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

// nspawnBase — общая часть запуска системы хоста в systemd-nspawn: корень, привязки WSLg, GPU-PV
// (/dev/dxg + d3d12 в Mesa хоста), общий /tmp, хук pacman. На диск хоста nspawn ничего не пишет:
// журнал, часовой пояс и resolv.conf не копируются.
func nspawnBase(p hostprofile.Profile) ([]string, error) {
	nspawn := []string{
		"--quiet", "--register=no",
		// Своё имя на каждый запуск: иначе оно берётся из каталога (root), и второй запуск
		// (оболочка при работающем рабочем столе) отказывается: «unix-export/root exists».
		"--machine=lolssd-" + strconv.Itoa(os.Getpid()),
		"--link-journal=no", "--timezone=off", "--resolv-conf=bind-host",
		"-D", rootDir,
		"--bind=/mnt/wslg:" + inner + "/wslg",
		"--bind-ro=/usr/lib/wsl:" + inner + "/lib",
		// /tmp хоста — каталог на его диске (в памяти он только при обычной загрузке хоста).
		// Один на все запуски, как в самой системе: оболочка и рабочий стол видят одно и то же.
		"--bind=" + sharedTmp + ":/tmp",
		"--bind=/mnt/wslg/.X11-unix:/tmp/.X11-unix",
	}
	err := os.MkdirAll(sharedTmp, 0o755)
	if err == nil {
		err = os.Chmod(sharedTmp, 0o777|os.ModeSticky) // как у /tmp: 1777
	}
	if err != nil {
		return nil, fmt.Errorf("/tmp: %w", err)
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
			return nil, fmt.Errorf("хук pacman: %w", err)
		}
		// overlay поверх /etc/pacman.d хоста: хук виден pacman, а на диск хоста не попадает.
		nspawn = append(nspawn, "--overlay=+/etc/pacman.d:"+upper+":/etc/pacman.d")
	} else {
		fmt.Fprintln(os.Stderr, "внимание: для", p.Distro.ID, "обновления ядра изнутри Windows не блокируются — не обновляйте систему отсюда")
	}
	return nspawn, nil
}

// runIn — программа системы хоста без её systemd (--as-pid2), с окружением WSLg: оболочка,
// отдельные программы, служебные шаги сеанса. extra — дополнительные аргументы nspawn.
func runIn(p hostprofile.Profile, extra, args []string) int {
	nspawn, err := nspawnBase(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	nspawn = append(nspawn, "--as-pid2", "--user="+p.User.Name, "--chdir="+p.User.Home,
		"--setenv=XDG_RUNTIME_DIR="+inner+"/wslg/runtime-dir",
		"--setenv=WAYLAND_DISPLAY=wayland-0",
		"--setenv=DISPLAY=:0",
		"--setenv=PULSE_SERVER=unix:"+inner+"/wslg/PulseServer",
		"--setenv=GALLIUM_DRIVER=d3d12",
		"--setenv=LD_LIBRARY_PATH="+inner+"/lib/lib",
		"--setenv=LOLSSD=wsl")
	nspawn = append(append(append(nspawn, extra...), "--"), args...)

	cmd := exec.Command("systemd-nspawn", nspawn...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return exitCode(cmd.Run())
}

// exitCode переводит ошибку exec в код выхода.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "systemd-nspawn:", err)
	return 1
}
