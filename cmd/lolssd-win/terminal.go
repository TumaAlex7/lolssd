//go:build windows

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/winctl"
)

var (
	procAllocConsole = kernel32.NewProc("AllocConsole")
	procSetTitle     = kernel32.NewProc("SetConsoleTitleW")
)

// terminal — ярлык «Linux terminal»: вход в систему хоста в консоли, без рабочего стола. Нет ВМ —
// запускает её без окон (lolssd-win vm, с запросом UAC) и ждёт; затем открывает консоль Windows,
// соединённую со вторым COM-портом ВМ (getty на ttyS1, winctl.ConsolePort). Консоль — в режиме
// VT: цвета, клавиши и Ctrl+C уходят в Linux как есть. Окно можно закрыть: ВМ продолжает
// работать, выключается она изнутри (poweroff).
func terminal() int {
	addr := fmt.Sprintf("127.0.0.1:%d", winctl.ConsolePort)
	procAllocConsole.Call()
	procSetTitle.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("Linux"))))
	in, _ := os.OpenFile("CONIN$", os.O_RDWR, 0)
	out, _ := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if in == nil || out == nil {
		return fail("Нет консоли")
	}

	if !listening(winctl.ConsolePort) {
		if h, err := windows.OpenEvent(windows.SYNCHRONIZE, false, windows.StringToUTF16Ptr(showEvent)); err == nil {
			windows.CloseHandle(h) // ВМ запускается, порт вот-вот появится
		} else {
			exe, _ := os.Executable()
			if err := windows.ShellExecute(0, windows.StringToUTF16Ptr("runas"), windows.StringToUTF16Ptr(exe),
				windows.StringToUTF16Ptr("vm"), nil, windows.SW_HIDE); err != nil {
				return fail("Запуск ВМ: %v", err)
			}
		}
		fmt.Fprint(out, "Запускаю Linux…")
		for i := 0; !listening(winctl.ConsolePort); i++ {
			if i == 600 {
				return fail("ВМ не открыла консоль за 5 минут, журналы в %s", dataDir)
			}
			time.Sleep(500 * time.Millisecond)
		}
		fmt.Fprint(out, "\r\n")
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return fail("Консоль ВМ: %v", err)
	}
	defer c.Close()

	// Ввод: без построчного режима, эха и обработки Ctrl+C; клавиши — VT-последовательностями.
	// Вывод: VT-последовательности понимает сама консоль.
	const (
		enableProcessedInput    = 0x1
		enableLineInput         = 0x2
		enableEchoInput         = 0x4
		enableVirtualTermInput  = 0x200
		enableVirtualTermOutput = 0x4
		enableProcessedOutput   = 0x1
	)
	var mode uint32
	hin, hout := windows.Handle(in.Fd()), windows.Handle(out.Fd())
	windows.GetConsoleMode(hin, &mode)
	windows.SetConsoleMode(hin, mode&^(enableProcessedInput|enableLineInput|enableEchoInput)|enableVirtualTermInput)
	windows.GetConsoleMode(hout, &mode)
	windows.SetConsoleMode(hout, mode|enableProcessedOutput|enableVirtualTermOutput)
	windows.SetConsoleCP(65001)
	windows.SetConsoleOutputCP(65001)

	c.Write([]byte("\r")) // getty перерисует приглашение входа
	go io.Copy(c, in)
	io.Copy(out, c)
	return 0
}
