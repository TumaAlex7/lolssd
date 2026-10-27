//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lolssd/hostprofile"
	"lolssd/tui"
)

// menu — lolssd без аргументов: проверка компьютера сводкой сверху и действия списком.
// На новой системе (профиля для её machine-id нет) первым идёт подготовка Linux для ВМ.
func menu() int {
	for {
		var header []string
		fails, warns := 0, 0
		runChecks(func(status, name, detail string) {
			switch status {
			case "FAIL":
				fails++
				header = append(header, tui.Red+"✗ "+name+": "+detail+"\x1b[0m")
			case "WARN":
				warns++
			}
		})
		switch {
		case fails == 0 && warns == 0:
			header = append([]string{tui.Green + "✓ Компьютер подходит" + "\x1b[0m"}, header...)
		case fails == 0:
			header = append([]string{fmt.Sprintf("%s✓ Компьютер подходит, замечаний: %d (подробно — «Проверка компьютера»)\x1b[0m", tui.Amber, warns)}, header...)
		}

		profileItem := "Подготовить Linux для работы внутри Windows"
		switch state := profileState(); state {
		case "":
			header = append(header, tui.Amber+"• Эта система здесь впервые: Linux внутри Windows ещё не подготовлен"+"\x1b[0m")
		case "ok":
			profileItem = "Обновить подготовку Linux внутри Windows"
		default:
			header = append(header, tui.Amber+"• "+state+"\x1b[0m")
			profileItem = "Обновить подготовку Linux внутри Windows"
		}

		type action struct {
			label string
			run   func()
		}
		actions := []action{
			{"Загрузиться в Windows", func() { sudo("boot") }},
			{profileItem, func() { runProfile(); tui.Pause() }},
			{"Добавить драйверы для Windows", func() { addDrivers(nil); tui.Pause() }},
			{"Проверка компьютера", func() { check(); tui.Pause() }},
			{"Убрать запись lolssd из загрузочного меню (NVRAM)", func() { sudo("boot", "--cleanup"); tui.Pause() }},
			{"Выход", nil},
		}
		if profileState() == "" {
			actions[0], actions[1] = actions[1], actions[0]
		}
		labels := make([]string, len(actions))
		for i, a := range actions {
			labels[i] = a.label
		}
		i := tui.Menu("lolssd — Windows на этом диске", header, labels)
		if i < 0 || actions[i].run == nil {
			return 0
		}
		actions[i].run()
	}
}

// profileState: "" — профиля этой системы нет; "ok" — есть и годится; иначе — что с ним не так.
func profileState() string {
	dir, err := lolssdDir()
	if err != nil {
		return "ok" // не с раздела LOLSSD: подготовка всё равно некуда
	}
	id, _ := os.ReadFile("/etc/machine-id")
	b, err := os.ReadFile(filepath.Join(dir, "profiles", strings.TrimSpace(string(id))+".json"))
	if err != nil {
		return ""
	}
	var p hostprofile.Profile
	if json.Unmarshal(b, &p) != nil || p.VM == nil {
		return "Подготовка Linux внутри Windows неполная: её стоит обновить"
	}
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	if k := strings.TrimSpace(string(kernel)); k != p.VM.Kernel {
		return "Ядро обновилось (" + p.VM.Kernel + " → " + k + "): обновите подготовку Linux внутри Windows"
	}
	return "ok"
}

// sudo запускает lolssd с аргументами от root (нужно для NVRAM).
func sudo(args ...string) {
	exe, err := os.Executable()
	if err == nil && os.Geteuid() == 0 {
		cmd := exec.Command(exe, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		cmd.Run()
		return
	}
	if err == nil {
		cmd := exec.Command("sudo", append([]string{exe}, args...)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
	}
	if err != nil {
		fmt.Println("не получилось:", err)
	}
}

// addDrivers копирует драйверы в LOLSSD/drivers. Windows при загрузке ставит из этой папки
// пакеты INF сама (lolssd-drivers.ps1), а установщики .exe и .msi выкладывает в папку
// Drivers на рабочем столе — их запускает пользователь. Без путей спрашивает их по одному.
func addDrivers(paths []string) int {
	dir, err := lolssdDir()
	if err != nil {
		fmt.Println("lolssd запущен не с раздела LOLSSD:", err)
		return 1
	}
	dst := filepath.Join(dir, "drivers")
	if len(paths) == 0 {
		fmt.Println("Папки с драйверами (.inf с файлами) или установщики .exe и .msi.")
		fmt.Println("Путь можно перетащить в окно терминала. Пустая строка — закончить.")
		for {
			p := tui.Input("Путь", "")
			if p == "" {
				break
			}
			paths = append(paths, p)
		}
	}
	failed := 0
	for _, p := range paths {
		p = strings.Trim(strings.TrimSpace(p), `'"`)
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[2:])
		}
		infs, setups, err := copyDrivers(p, filepath.Join(dst, filepath.Base(p)))
		switch {
		case err != nil:
			failed++
			fmt.Printf("  ✗ %s: %v\n", p, err)
		case infs == 0 && setups == 0:
			fmt.Printf("  • %s: скопировано, но ни .inf, ни .exe/.msi в нём нет\n", p)
		default:
			fmt.Printf("  ✓ %s: пакетов INF %d (поставятся сами при загрузке Windows), установщиков %d (будут в папке Drivers на рабочем столе)\n", p, infs, setups)
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// copyDrivers копирует файл или папку src в dst и считает пакеты INF и установщики.
func copyDrivers(src, dst string) (infs, setups int, err error) {
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".inf":
			infs++
		case ".exe", ".msi":
			setups++
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(to)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	return infs, setups, err
}
