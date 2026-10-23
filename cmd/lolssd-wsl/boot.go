//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lolssd/hostprofile"
)

// Рабочий стол — настоящий вход в систему хоста: контейнер загружает её systemd как PID 1 в
// обычную multi-user.target (все включённые сервисы, таймеры, сокеты, постоянный журнал, /tmp —
// как при загрузке с диска), logind открывает сеанс пользователя и поднимает user@UID, а
// Hyprland работает в этом сеансе вместо дисплейного менеджера.
//
// Отличия от загрузки с диска — только там, где железом владеют Windows и пусковой дистрибутив
// (юниты и маски — в /run/systemd/system контейнера):
//   - fstab и crypttab пустые: диски уже смонтированы и расшифрованы пусковым дистрибутивом, а
//     устройств для повторного монтирования в контейнере нет — systemd ждал бы их и ушёл в
//     аварийный режим;
//   - NetworkManager, networkd, iwd, wpa_supplicant замаскированы: сетью WSL владеет Windows
//     (в режиме mirrored это её же адаптеры);
//   - graphical.target (дисплейный менеджер) не загружается — его роль играет lolssd-hyprland.
// Обновления ядра блокирует хук pacman (nspawnBase).

const desktopTarget = "lolssd-desktop.target"

var hostMasks = []string{"NetworkManager.service", "NetworkManager-wait-online.service",
	"systemd-networkd.service", "systemd-networkd.socket", "systemd-networkd-wait-online.service",
	"iwd.service", "wpa_supplicant.service"}

// bootDesktop запускает контейнер с systemd хоста, в котором сервис lolssd-hyprland от имени
// пользователя выполняет command с окружением env. Контейнер завершается вместе с ним и с его
// кодом выхода. Вывод системы и Hyprland идёт в консоль контейнера, то есть в stdout.
func bootDesktop(p hostprofile.Profile, extra, env, command []string) (*exec.Cmd, error) {
	nspawn, err := nspawnBase(p)
	if err != nil {
		return nil, err
	}
	run := fmt.Sprintf("/run/lolssd/boot-%d", os.Getpid())
	os.RemoveAll(run)
	units := filepath.Join(run, "units")
	if err := os.MkdirAll(units, 0o755); err != nil {
		return nil, err
	}
	for _, m := range hostMasks {
		if err := os.Symlink("/dev/null", filepath.Join(units, m)); err != nil {
			return nil, err
		}
	}
	files := map[string]string{
		filepath.Join(units, desktopTarget):             targetUnit,
		filepath.Join(units, "lolssd-hyprland.service"): hyprlandUnit(p, env, command),
		filepath.Join(run, "empty"):                     "",
	}
	for path, text := range files {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return nil, err
		}
	}
	nspawn = append(nspawn, "--boot", "--console=read-only",
		// Журнал (--link-journal=no в nspawnBase) systemd хоста ведёт сам, в своём
		// /var/log/journal на диске, как при обычной загрузке.
		"--bind-ro="+units+":/run/systemd/system")
	// Только существующие файлы: привязка к несуществующему создала бы его на диске хоста.
	for _, f := range []string{"etc/fstab", "etc/crypttab"} {
		if _, err := os.Stat(filepath.Join(rootDir, f)); err == nil {
			nspawn = append(nspawn, "--bind-ro="+filepath.Join(run, "empty")+":/"+f)
		}
	}
	nspawn = append(append(nspawn, extra...), "--", "systemd.unit="+desktopTarget)

	cmd := exec.Command("systemd-nspawn", nspawn...)
	cmd.Env = nspawnEnv()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd, cmd.Start()
}

const targetUnit = `[Unit]
Description=lolssd: рабочий стол хоста в Windows
Requires=multi-user.target lolssd-hyprland.service
After=multi-user.target
AllowIsolate=yes
`

// hyprlandUnit — сервис сеанса: вход через PAM (login), как у дисплейного менеджера, поэтому
// logind регистрирует сеанс и запускает user@UID. Выход Hyprland завершает контейнер с его кодом.
func hyprlandUnit(p hostprofile.Profile, env, command []string) string {
	var b strings.Builder
	b.WriteString(`[Unit]
Description=lolssd: Hyprland хоста
Wants=dbus.socket systemd-logind.service systemd-user-sessions.service
After=dbus.socket systemd-logind.service systemd-user-sessions.service
SuccessAction=exit
FailureAction=exit

[Service]
Type=simple
User=` + p.User.Name + `
PAMName=login
WorkingDirectory=~
StandardOutput=journal+console
StandardError=journal+console
Environment=XDG_SESSION_TYPE=wayland XDG_SESSION_CLASS=user XDG_CURRENT_DESKTOP=Hyprland LOLSSD=wsl
`)
	// Сеанс из сервиса без терминала pam_systemd записывает в класс background, а такому
	// XDG_RUNTIME_DIR не выдаётся («XDG_RUNTIME_DIR is not set!»). Поэтому менеджер пользователя
	// (и с ним /run/user/UID) запускается явно, от root (+), а окружение задаётся прямо.
	uid := strconv.Itoa(p.User.UID)
	b.WriteString("ExecStartPre=+/usr/bin/systemctl start user@" + uid + ".service\n")
	b.WriteString("Environment=XDG_RUNTIME_DIR=/run/user/" + uid + " DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/" + uid + "/bus\n")
	for _, e := range env {
		b.WriteString("Environment=" + strconv.Quote(e) + "\n")
	}
	var args []string
	for _, a := range command {
		args = append(args, strconv.Quote(a))
	}
	b.WriteString("ExecStart=" + strings.Join(args, " ") + "\n")
	return b.String()
}

// containerInit ждёт, пока nspawn запустит PID 1 контейнера, и возвращает его номер в
// пространстве имён пускового дистрибутива: через /proc/<pid>/root видны его /run и сокеты.
func containerInit(nspawn int) (int, error) {
	for range 100 {
		tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", nspawn))
		for _, t := range tasks {
			b, _ := os.ReadFile(t)
			// Сначала там мелькает промежуточный процесс nspawn; PID 1 контейнера — это systemd.
			for _, f := range strings.Fields(string(b)) {
				comm, _ := os.ReadFile("/proc/" + f + "/comm")
				if pid, err := strconv.Atoi(f); err == nil && strings.TrimSpace(string(comm)) == "systemd" {
					return pid, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, fmt.Errorf("systemd-nspawn (%d) не запустил контейнер", nspawn)
}
