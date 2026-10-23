//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// (/dev/dxg + d3d12 в Mesa хоста), общий /tmp, хук pacman.
//
// Контейнер без ограничений — система хоста должна вести себя как в виртуальной машине, а не
// как в песочнице: все capabilities, без фильтра системных вызовов, /sys и /proc/sys на запись
// (переменные окружения nspawn — в nspawnEnv), все устройства ВМ WSL (devBinds). Сеть общая с
// пусковым дистрибутивом. Своё у неё только дерево процессов: PID 1 — systemd хоста (рабочий стол).
func nspawnBase(p hostprofile.Profile) ([]string, error) {
	nspawn := []string{
		"--quiet", "--register=no", "--capability=all",
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
	if _, err := os.Stat("/dev/dxg"); err != nil {
		fmt.Fprintln(os.Stderr, "внимание: нет /dev/dxg, графика без ускорения")
	}
	nspawn = append(nspawn, devBinds()...)
	nspawn = append(nspawn, devAllow()...)
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

// nspawnStd — устройства, которые nspawn создаёт в контейнере сам; остальные пробрасываются.
var nspawnStd = map[string]bool{
	"null": true, "zero": true, "full": true, "random": true, "urandom": true, "tty": true,
	"console": true, "ptmx": true, "pts": true, "shm": true, "mqueue": true, "hugepages": true,
	"fd": true, "stdin": true, "stdout": true, "stderr": true, "core": true, "log": true,
	"initctl": true, "char": true, "block": true,
}

// devBinds — все устройства ВМ WSL для контейнера: видеокарта (dxg, dri), диски хоста,
// device-mapper, fuse, kvm, tun и прочее, что есть в /dev пускового дистрибутива. Каталоги
// пробрасываются целиком; консоли и терминалы (tty*) — нет, у контейнера свои.
func devBinds() []string {
	entries, _ := os.ReadDir("/dev")
	var binds []string
	for _, e := range entries {
		name := e.Name()
		if nspawnStd[name] || strings.HasPrefix(name, "tty") || strings.HasPrefix(name, "vcs") {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue // ссылки (fd, stdin…) nspawn создаёт сам, а цели пробрасываются как есть
		}
		binds = append(binds, "--bind=/dev/"+name)
	}
	return binds
}

// nspawnCmd — команда systemd-nspawn. Берётся из системы хоста, если он там есть, и запускается
// её же загрузчиком с её библиотеками (glibc пускового Debian старше): nspawn должен быть той же
// версии, что systemd, который он загружает. Иначе, например, nspawn 257 подменяет boot ID
// удаляемым файлом, systemd 262 не может перенести эту подмену в свежий /proc служб с песочницей,
// и logind отклоняет вход («JSON field 'PID' refers to non-local PID»). Нет своего — Debian-овский.
func nspawnCmd() []string {
	bin := filepath.Join(rootDir, "usr/bin/systemd-nspawn")
	loaders, _ := filepath.Glob(filepath.Join(rootDir, "usr/lib*/ld-linux-x86-64.so.2"))
	if more, _ := filepath.Glob(filepath.Join(rootDir, "lib*/ld-linux-x86-64.so.2")); len(loaders) == 0 {
		loaders = more
	}
	if _, err := os.Stat(bin); err != nil || len(loaders) == 0 {
		return []string{"systemd-nspawn"}
	}
	var libs []string
	for _, d := range []string{"usr/lib", "usr/lib64", "usr/lib/x86_64-linux-gnu", "usr/lib/systemd", "usr/lib64/systemd", "usr/lib/x86_64-linux-gnu/systemd"} {
		if st, err := os.Stat(filepath.Join(rootDir, d)); err == nil && st.IsDir() {
			libs = append(libs, filepath.Join(rootDir, d))
		}
	}
	return []string{loaders[0], "--library-path", strings.Join(libs, ":"), bin}
}

// devAllow — доступ ко всем устройствам. Проброса узлов мало: nspawn пускает к ним через свой
// cgroup-фильтр только по белому списку, остальное — EPERM (так падали vgem и /dev/mapper/control).
// Разрешаются все группы устройств ядра из /proc/devices, символьные и блочные.
func devAllow() []string {
	b, _ := os.ReadFile("/proc/devices")
	args := []string{"--property=DevicePolicy=auto"}
	kind := ""
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "Character devices"):
			kind = "char-"
		case strings.HasPrefix(line, "Block devices"):
			kind = "block-"
		default:
			// Группы с «/» в имени (/dev/tty, /dev/console, /dev/vc/0) — терминалы, их nspawn даёт
			// сам; такое имя не годится для DeviceAllow=char-….
			if f := strings.Fields(line); kind != "" && len(f) == 2 && !strings.Contains(f[1], "/") {
				if a := "--property=DeviceAllow=" + kind + f[1] + " rwm"; !slices.Contains(args, a) {
					args = append(args, a)
				}
			}
		}
	}
	return args
}

// nspawnEnv — окружение для systemd-nspawn: без фильтра системных вызовов (seccomp) и с /sys,
// /proc/sys на запись.
func nspawnEnv() []string {
	return append(os.Environ(), "SYSTEMD_SECCOMP=0", "SYSTEMD_NSPAWN_API_VFS_WRITABLE=yes")
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

	c := nspawnCmd()
	cmd := exec.Command(c[0], append(c[1:], nspawn...)...)
	cmd.Env = nspawnEnv()
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
