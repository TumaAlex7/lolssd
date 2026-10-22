//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/winctl"
)

var (
	procSetDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procRegisterHotKey  = user32.NewProc("RegisterHotKey")
	procSetTimer        = user32.NewProc("SetTimer")
	procGetMessage      = user32.NewProc("GetMessageW")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
)

const (
	hotkeyToggle = 1
	wmHotkey     = 0x0312
	wmTimer      = 0x0113
	modAlt       = 0x1
	modControl   = 0x2
	modNoRepeat  = 0x4000
	vkF12        = 0x7b
)

type msg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

var _ [48]byte = [unsafe.Sizeof(msg{})]byte{} // MSG на amd64

// desktop — ярлык «Linux desktop (host)» (docs/ARCHITECTURE.md §6). Запускает в WSL
// lolssd-wsl session (Hyprland без экрана, по выходу и wayvnc на монитор) в видимой консоли:
// там ход первой сборки aquamarine, пароль LUKS и ошибки. На каждый монитор Windows, как
// только его wayvnc отвечает, открывает полноэкранный TigerVNC.
//
// Монитор отдают Windows, закрыв его просмотрщик (F8 → Exit viewer в TigerVNC, или
// Ctrl+Alt+F12, если Windows получит сочетание), и возвращают Linux сочетанием Ctrl+Alt+F12 на
// нём: Hyprland и его выход при этом продолжают работать. Всё заканчивается, когда выходят
// из Hyprland: тогда закрываются и просмотрщики.
func desktop() int {
	runtime.LockOSThread() // горячая клавиша и таймер привязаны к потоку
	// Координаты CCD API физические: процесс должен понимать DPI каждого монитора.
	const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	procSetDpiAwareness.Call(perMonitorAwareV2)

	viewer := filepath.Join(os.Getenv("ProgramFiles"), "lolssd", "vncviewer.exe")
	if _, err := os.Stat(viewer); err != nil {
		log.Println("нет просмотрщика VNC:", err)
		return 1
	}
	// Мониторы — текущие, а не с момента входа: раскладку могли поменять. Сеанс получает их
	// через WSLENV, без файла (в ProgramData у пользователя нет права записи).
	mons, err := activeMonitors()
	if err != nil {
		log.Println("мониторы:", err)
		return 1
	}
	monsJSON, _ := json.Marshal(mons)
	// Задание mount при входе срабатывает с задержкой, а ярлык могут открыть раньше: тогда
	// сеанс пошёл бы в старом пусковом дистрибутиве. Импорт — на пользователя, прав не требует.
	ensureDistro()

	// Вывод сеанса — и в консоль, и в session.log рядом с журналом (читается и из Linux).
	// При ошибке консоль ждёт Enter.
	script := `cd /mnt/c/ProgramData/lolssd && for f in session.log hyprland-*.log crash-*.txt wayvnc-*.log vncviewer-*.log; do [ -f "$f" ] && mv -f "$f" "prev-$f"; done
{ /usr/local/bin/lolssd-wsl session; echo $? >/run/lolssd-rc; } 2>&1 | tee /mnt/c/ProgramData/lolssd/session.log
rc=$(cat /run/lolssd-rc); [ "$rc" = 0 ] || { echo; echo "Ошибка (код $rc), вывод в C:\\ProgramData\\lolssd\\session.log. Enter — закрыть"; read x; }`
	os.Setenv("LOLSSD_MONITORS", string(monsJSON))
	os.Setenv("WSLENV", os.Getenv("WSLENV")+":LOLSSD_MONITORS")
	sess, err := startConsole("wsl.exe", "-d", "lolssd", "-u", "root", "--", "/bin/sh", "-c", script)
	if err != nil {
		log.Println("wsl.exe:", err)
		return 1
	}
	sessDone := make(chan struct{})
	go func() { windows.WaitForSingleObject(sess, windows.INFINITE); close(sessDone) }()
	log.Printf("сеанс запущен, мониторов %d", len(mons))

	if r, _, err := procRegisterHotKey.Call(0, hotkeyToggle, modControl|modAlt|modNoRepeat, vkF12); r == 0 {
		log.Println("RegisterHotKey Ctrl+Alt+F12:", err) // не мешает: остаётся F8 в TigerVNC
	}
	procSetTimer.Call(0, 0, 500, 0)

	viewers := make([]*exec.Cmd, len(mons))
	started := make([]time.Time, len(mons))
	given := make([]bool, len(mons)) // отдан Windows
	exited := make(chan int, len(mons))
	open := func(i int) {
		c := exec.Command(viewer, winctl.ViewerArgs(mons[i], i)...)
		// Журнал просмотрщика: выбранное кодирование, оценка скорости канала и смены качества
		// JPEG по замеренной пропускной способности. У оконной программы stderr иначе никуда не идёт.
		if f, err := os.OpenFile(filepath.Join(os.Getenv("ProgramData"), "lolssd", fmt.Sprintf("vncviewer-%d.log", i+1)),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			c.Stdout, c.Stderr = f, f
			defer f.Close()
		}
		if err := c.Start(); err != nil {
			log.Println("vncviewer:", err)
			return
		}
		viewers[i], started[i] = c, time.Now()
		go func() { c.Wait(); exited <- i }()
	}
	closeAll := func() {
		for _, c := range viewers {
			if c != nil {
				c.Process.Kill()
			}
		}
	}

	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			closeAll()
			return 0
		}
		switch m.Message {
		case wmTimer:
			select {
			case <-sessDone:
				closeAll()
				log.Println("сеанс завершён")
				return 0
			default:
			}
		drain:
			for {
				select {
				case i := <-exited:
					// Закрыли сами — монитор отдан Windows. Вылетел сразу после запуска — wayvnc
					// ещё не принимал, откроем снова.
					// Сервер в этот момент не отвечает — значит, упал wayvnc, а не закрыл пользователь:
					// откроем снова, когда сеанс его перезапустит.
					if time.Since(started[i]) > 3*time.Second && listening(winctl.VNCPort(i)) {
						given[i] = true
						log.Printf("монитор %d отдан Windows", i+1)
					}
					viewers[i] = nil
				default:
					break drain
				}
			}
			for i := range mons {
				if viewers[i] == nil && !given[i] && listening(winctl.VNCPort(i)) {
					open(i)
				}
			}

		case wmHotkey:
			var pt struct{ X, Y int32 }
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			k := winctl.MonitorAt(mons, int(pt.X), int(pt.Y))
			if k < 0 {
				continue
			}
			if viewers[k] != nil {
				viewers[k].Process.Kill() // дальше как при закрытии: given[k] выставит таймер
				started[k] = time.Time{}
			} else {
				given[k] = false
				log.Printf("монитор %d возвращён Linux", k+1)
			}
		}
	}
}

// startConsole запускает программу в новой консоли с её собственными вводом и выводом.
// os/exec так не умеет: незаданные Stdin/Stdout/Stderr он подставляет как NUL, и консоль
// остаётся пустой, а read в ней сразу получает конец файла.
func startConsole(args ...string) (windows.Handle, error) {
	exe, err := exec.LookPath(args[0])
	if err != nil {
		return 0, err
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	cmdline, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	app, _ := windows.UTF16PtrFromString(exe)
	if err := windows.CreateProcess(app, cmdline, nil, nil, false, windows.CREATE_NEW_CONSOLE, nil, nil, &si, &pi); err != nil {
		return 0, err
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

func listening(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
