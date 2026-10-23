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
//   - graphical.target (дисплейный менеджер) не загружается, а console-getty замаскирован —
//     вход на консоли делает lolssd-hyprland, как на tty1.
// Обновления ядра блокирует хук pacman (nspawnBase).

const desktopTarget = "lolssd-desktop.target"

var hostMasks = []string{"NetworkManager.service", "NetworkManager-wait-online.service",
	"systemd-networkd.service", "systemd-networkd.socket", "systemd-networkd-wait-online.service",
	"iwd.service", "wpa_supplicant.service",
	// Приглашение входа на консоли контейнера: консоль занимает сеанс Hyprland (как tty1).
	"console-getty.service"}

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
	units, polkit := filepath.Join(run, "units"), filepath.Join(run, "polkit")
	userUnits := filepath.Join(run, "user-units")
	for _, d := range []string{units, polkit, filepath.Join(userUnits, "default.target.wants")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	for _, m := range hostMasks {
		if err := os.Symlink("/dev/null", filepath.Join(units, m)); err != nil {
			return nil, err
		}
	}
	files := map[string]string{
		filepath.Join(polkit, "49-lolssd.rules"):              polkitRule(p.User.Name),
		filepath.Join(userUnits, "lolssd-wslg-audio.service"): audioUnit,
		filepath.Join(units, desktopTarget):                   targetUnit,
		filepath.Join(units, "lolssd-hyprland.service"):       hyprlandUnit(p, env, command),
		filepath.Join(run, "empty"):                           "",
	}
	for path, text := range files {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return nil, err
		}
	}
	if err := os.Symlink("../lolssd-wslg-audio.service", filepath.Join(userUnits, "default.target.wants", "lolssd-wslg-audio.service")); err != nil {
		return nil, err
	}
	nspawn = append(nspawn, "--boot", "--console=read-only",
		// Журнал (--link-journal=no в nspawnBase) systemd хоста ведёт сам, в своём
		// /var/log/journal на диске, как при обычной загрузке.
		"--bind-ro="+units+":/run/systemd/system",
		"--bind-ro="+polkit+":/run/polkit-1/rules.d",
		"--bind-ro="+userUnits+":/run/systemd/user")
	// Только существующие файлы: привязка к несуществующему создала бы его на диске хоста.
	for _, f := range []string{"etc/fstab", "etc/crypttab"} {
		if _, err := os.Stat(filepath.Join(rootDir, f)); err == nil {
			nspawn = append(nspawn, "--bind-ro="+filepath.Join(run, "empty")+":/"+f)
		}
	}
	nspawn = append(append(nspawn, extra...), "--", "systemd.unit="+desktopTarget)

	c := nspawnCmd()
	cmd := exec.Command(c[0], append(c[1:], nspawn...)...)
	cmd.Env = nspawnEnv()
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd, cmd.Start()
}

// polkitRule — пользователю сеанса разрешено всё, что спрашивает polkit (выключение, монтирование,
// systemctl и т. д.). Процессы рабочего стола живут в сервисе lolssd-hyprland, а не в scope сеанса,
// и для polkit они не «активный локальный сеанс» — без правила poweroff требовал бы пароль.
// Только в этом режиме: правило — в /run/polkit-1/rules.d контейнера (polkit ≥ 125), на диске
// хоста его нет.
func polkitRule(user string) string {
	return "// lolssd: рабочий стол хоста в Windows\npolkit.addRule(function(action, subject) {\n" +
		"  if (subject.user == " + strconv.Quote(user) + ") return polkit.Result.YES;\n});\n"
}

// audioUnit — пользовательский юнит: звук PipeWire уходит в Windows. Звуковых устройств у ВМ WSL
// нет, есть только PulseAudio-сервер WSLg, а PipeWire хоста о нём не знает (громкость 100 %,
// звука нет). pipewire-pulse поднимает к нему туннели вывода и записи и делает их устройствами
// по умолчанию. Только в этом режиме: юнит — в /run/systemd/user контейнера.
var audioUnit = `[Unit]
Description=lolssd: звук через Windows (PulseAudio-сервер WSLg)
Wants=pipewire-pulse.socket wireplumber.service
After=pipewire-pulse.socket wireplumber.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStartPre=/bin/sh -c 'for i in $(seq 100); do pactl info >/dev/null 2>&1 && exit 0; sleep 0.2; done; exit 1'
ExecStart=/usr/bin/pactl load-module module-tunnel-sink server=unix:` + inner + `/wslg/PulseServer sink_name=windows sink_properties=device.description=Windows
ExecStart=/usr/bin/pactl load-module module-tunnel-source server=unix:` + inner + `/wslg/PulseServer source_name=windows-mic source_properties=device.description=Windows-mic
ExecStart=/usr/bin/pactl set-default-sink windows
ExecStart=/usr/bin/pactl set-default-source windows-mic

[Install]
WantedBy=default.target
`

const targetUnit = `[Unit]
Description=lolssd: рабочий стол хоста в Windows
Requires=multi-user.target lolssd-hyprland.service
After=multi-user.target
AllowIsolate=yes
`

// hyprlandUnit — сервис сеанса: вход через PAM (login) на консоли контейнера, как вход на tty1,
// поэтому logind регистрирует обычный сеанс пользователя и запускает user@UID. Выход Hyprland завершает контейнер с его кодом.
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
TTYPath=/dev/console
StandardInput=tty
StandardOutput=journal+console
StandardError=journal+console
TTYReset=no
TTYVHangup=no
Environment=XDG_SESSION_TYPE=wayland XDG_SESSION_CLASS=user XDG_CURRENT_DESKTOP=Hyprland LOLSSD=wsl
`)
	// Сеанс без терминала pam_systemd записывает в класс background: без XDG_RUNTIME_DIR и не
	// активный для polkit. Поэтому у сервиса есть терминал — консоль контейнера, как tty1 при входе
	// на железе: сеанс получает класс user, и logind сам поднимает /run/user/UID и user@UID.
	// Запускать user@UID вручную нельзя: до сеанса его pam_systemd получает от logind отказ
	// (CreateSession: InvalidParameter), и падал бы весь вход.
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
