//go:build windows

package main

import (
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/hostprofile"
	"lolssd/winctl"
)

var (
	procSetDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procRegisterHotKey  = user32.NewProc("RegisterHotKey")
	procSetTimer        = user32.NewProc("SetTimer")
	procGetMessage      = user32.NewProc("GetMessageW")
	procGetWindowText   = user32.NewProc("GetWindowTextW")
	procIsWindowVisible = user32.NewProc("IsWindowVisible")
	procIsIconic        = user32.NewProc("IsIconic")
	procShowWindow      = user32.NewProc("ShowWindow")
	procSetWindowPos    = user32.NewProc("SetWindowPos")
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
)

const (
	hotkeyToggle = 1
	wmHotkey     = 0x0312
	wmTimer      = 0x0113
	modAlt       = 0x1
	modControl   = 0x2
	modNoRepeat  = 0x4000
	vkF12        = 0x7b
	swMinimize   = 6
	swRestore    = 9
)

type msg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type rect struct{ Left, Top, Right, Bottom int32 }

// controller держит окна вложенных выходов Hyprland (docs/ARCHITECTURE.md §6, «Linux поверх
// Windows, отдельно на каждом мониторе»): окно выхода N — на весь экран на мониторе N Windows.
// Ctrl+Alt+F12 отдаёт монитор под курсором Windows (окно Linux сворачивается) или
// возвращает его Linux. Выход в Hyprland при этом не удаляется: пересозданный выход получил бы
// новое имя, и переведённые правила конфига перестали бы к нему относиться.
// Работает в сеансе пользователя с момента входа; пока окон Linux нет — просто ждёт.
func controller() int {
	runtime.LockOSThread() // горячая клавиша и таймер привязаны к потоку
	// Координаты CCD API физические: процесс должен понимать DPI каждого монитора.
	const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	procSetDpiAwareness.Call(perMonitorAwareV2)

	if r, _, err := procRegisterHotKey.Call(0, hotkeyToggle, modControl|modAlt|modNoRepeat, vkF12); r == 0 {
		log.Println("RegisterHotKey Ctrl+Alt+F12:", err)
		return 1
	}
	procSetTimer.Call(0, 0, 500, 0)

	var slots []uintptr
	given := map[int]bool{} // мониторы, отданные Windows
	var mons []hostprofile.WinMonitor
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return 0
		}
		switch m.Message {
		case wmTimer:
			if ms, err := activeMonitors(); err == nil {
				mons = ms
			}
			alive, titles := outputWindows()
			slots = winctl.Assign(slots, alive, titles)
			place(slots, mons, given)

		case wmHotkey:
			var pt struct{ X, Y int32 }
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			k := winctl.MonitorAt(mons, int(pt.X), int(pt.Y))
			if k < 0 || k >= len(slots) || slots[k] == 0 {
				continue
			}
			given[k] = !given[k]
			if given[k] {
				procShowWindow.Call(slots[k], swMinimize)
				log.Printf("монитор %d отдан Windows", k+1)
			} else {
				procShowWindow.Call(slots[k], swRestore)
				procSetForeground.Call(slots[k])
				log.Printf("монитор %d возвращён Linux", k+1)
			}
			place(slots, mons, given)
		}
	}
}

// place ставит окна выходов на свои мониторы. Окно, которое пользователь развернул сам
// (например, с панели задач), значит, что монитор снова у Linux.
func place(slots []uintptr, mons []hostprofile.WinMonitor, given map[int]bool) {
	for k, h := range slots {
		if h == 0 || k >= len(mons) {
			continue
		}
		iconic, _, _ := procIsIconic.Call(h)
		if given[k] {
			if iconic == 0 {
				given[k] = false
			} else {
				continue
			}
		}
		if iconic != 0 {
			continue // свёрнуто пользователем по-другому — не мешаем
		}
		mon := mons[k]
		var r rect
		procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		if int(r.Left) == mon.X && int(r.Top) == mon.Y && int(r.Right-r.Left) == mon.Width && int(r.Bottom-r.Top) == mon.Height {
			continue
		}
		const swpNoZOrder, swpNoActivate = 0x4, 0x10
		procSetWindowPos.Call(h, 0, uintptr(mon.X), uintptr(mon.Y), uintptr(mon.Width), uintptr(mon.Height), swpNoZOrder|swpNoActivate)
	}
}

// Состояние перечисления окон. Обратный вызов создаётся один раз: windows.NewCallback
// не освобождается, а число таких вызовов в процессе ограничено.
var (
	enumAlive  []uintptr
	enumTitles map[uintptr]string
	enumCb     = windows.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		buf := make([]uint16, 256)
		n, _, _ := procGetWindowText.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		title := windows.UTF16ToString(buf[:n])
		if !winctl.IsOutputWindow(title) || !strings.EqualFold(processName(h), "msrdc.exe") {
			return 1
		}
		if v, _, _ := procIsWindowVisible.Call(uintptr(h)); v == 0 {
			return 1
		}
		enumAlive = append(enumAlive, uintptr(h))
		enumTitles[uintptr(h)] = title
		return 1
	})
)

// outputWindows находит окна WSLg (процесс msrdc.exe), похожие на выходы Hyprland,
// в порядке перечисления, с их заголовками.
func outputWindows() ([]uintptr, map[uintptr]string) {
	enumAlive, enumTitles = nil, map[uintptr]string{}
	windows.EnumWindows(enumCb, nil)
	return enumAlive, enumTitles
}

func processName(h windows.HWND) string {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(h, &pid); err != nil {
		return ""
	}
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(p)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(p, 0, &buf[0], &n) != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}

var _ [48]byte = [unsafe.Sizeof(msg{})]byte{} // MSG на amd64
