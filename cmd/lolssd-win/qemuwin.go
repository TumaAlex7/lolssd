//go:build windows

package main

import (
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/hostprofile"
)

var (
	procSetWindowPos  = user32.NewProc("SetWindowPos")
	procGetWindowText = user32.NewProc("GetWindowTextW")
	procShowWindow    = user32.NewProc("ShowWindow")
	procSetForeground = user32.NewProc("SetForegroundWindow")
)

// qemuWindow — окно QEMU (SDL) и номер выхода, который оно показывает.
type qemuWindow struct {
	hwnd  windows.HWND
	head  int
	title string
}

// SDL в QEMU подписывает окна выходов «QEMU (имя-N)», где N — номер консоли (выхода).
var headTitle = regexp.MustCompile(`-(\d+)\)`)

// processWindows — видимые окна верхнего уровня процесса pid, в порядке создания.
func processWindows(pid int) []qemuWindow {
	var res []qemuWindow
	cb := windows.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		var p uint32
		windows.GetWindowThreadProcessId(h, &p)
		if int(p) == pid && windows.IsWindowVisible(h) {
			buf := make([]uint16, 256)
			n, _, _ := procGetWindowText.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			res = append(res, qemuWindow{hwnd: h, head: -1, title: windows.UTF16ToString(buf[:n])})
		}
		return 1
	})
	windows.EnumWindows(cb, nil)
	// EnumWindows идёт сверху вниз по Z-порядку: новые окна сверху — переворачиваем.
	for i, j := 0, len(res)-1; i < j; i, j = i+1, j-1 {
		res[i], res[j] = res[j], res[i]
	}
	for i := range res {
		res[i].head = i
		if m := headTitle.FindStringSubmatch(res[i].title); m != nil {
			res[i].head, _ = strconv.Atoi(m[1])
		}
	}
	sort.SliceStable(res, func(a, b int) bool { return res[a].head < res[b].head })
	return res
}

// placeWindows ставит окно каждого выхода на весь его монитор Windows (выходы — слева направо,
// как мониторы). Окна SDL на весь экран, поэтому это просто их прямоугольник: QEMU сообщает
// гостю новый размер выхода сам.
func placeWindows(ws []qemuWindow, mons []hostprofile.WinMonitor) {
	const swpNoZOrder, swpShowWindow, swpFrameChanged = 0x4, 0x40, 0x20
	for i, w := range ws {
		if i >= len(mons) {
			break
		}
		m := mons[i]
		procSetWindowPos.Call(uintptr(w.hwnd), 0, uintptr(m.X), uintptr(m.Y), uintptr(m.Width), uintptr(m.Height),
			swpNoZOrder|swpShowWindow|swpFrameChanged)
		log.Printf("окно %q → монитор %d (%d,%d %dx%d)", w.title, i+1, m.X, m.Y, m.Width, m.Height)
	}
}

// qemuWindowsLoop — режим окон QEMU после запуска ВМ: дождаться окон всех выходов и расставить их,
// потом ждать выключения ВМ. Повторный запуск ярлыка (событие ev) разворачивает свёрнутые окна.
func qemuWindowsLoop(proc *os.Process, vmDone <-chan struct{}, mons []hostprofile.WinMonitor, ev windows.Handle) int {
	placed := false
	for start := time.Now(); ; {
		select {
		case <-vmDone:
			log.Println("ВМ выключена")
			return 0
		case <-time.After(500 * time.Millisecond):
		}
		ws := processWindows(proc.Pid)
		if !placed && (len(ws) >= len(mons) || time.Since(start) > 60*time.Second && len(ws) > 0) {
			if len(ws) < len(mons) {
				log.Printf("окон QEMU %d, мониторов %d", len(ws), len(mons))
			}
			placeWindows(ws, mons)
			placed = true
		}
		if r, _ := windows.WaitForSingleObject(ev, 0); r == windows.WAIT_OBJECT_0 {
			for _, w := range ws {
				procShowWindow.Call(uintptr(w.hwnd), 9) // SW_RESTORE
			}
			if len(ws) > 0 {
				procSetForeground.Call(uintptr(ws[0].hwnd))
			}
			log.Println("окна QEMU развёрнуты")
		}
	}
}
