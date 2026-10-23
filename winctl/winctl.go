// Package winctl — логика рабочего стола Linux на стороне Windows без вызовов ОС, чтобы её
// можно было проверять тестами на Linux. Сама команда — cmd/lolssd-win/desktop.go.
package winctl

import (
	"fmt"

	"lolssd/hostprofile"
)

// VNCPort — порт wayvnc выхода HEADLESS-(i+1); та же нумерация в cmd/lolssd-wsl/desktop.go.
func VNCPort(i int) int { return 5901 + i }

// ViewerArgs — аргументы TigerVNC для монитора i: окно открывается на этом мониторе и сразу
// уходит в полноэкранный режим на нём же (FullScreenMode=Current). В полноэкранном режиме
// TigerVNC забирает всю клавиатуру, включая Win, — она становится Super в Hyprland.
// Размер экрана задаёт Hyprland по монитору, поэтому RemoteResize выключен; wayvnc слушает
// только 127.0.0.1, без пароля.
func ViewerArgs(m hostprofile.WinMonitor, i int) []string {
	return []string{
		"-FullScreen=1", "-FullScreenMode=Current", "-RemoteResize=0",
		// Канал локальный: без автоподбора и JPEG — Tight без потерь, полный цвет, слабый zlib
		// (меньше работы процессору на обеих сторонах).
		"-AutoSelect=0", "-PreferredEncoding=Tight", "-NoJPEG=1", "-FullColor=1", "-CompressLevel=1",
		"-SecurityTypes=None", "-AlertOnFatalError=0", "-MenuKey=F8",
		// Viewport:100 — каждое нажатие клавиши и во что оно превратилось (проверка клавиши меню).
		"-Log=*:stderr:30,Viewport:stderr:100",
		fmt.Sprintf("-geometry=640x480+%d+%d", m.X+m.Width/4, m.Y+m.Height/4),
		fmt.Sprintf("127.0.0.1::%d", VNCPort(i)),
	}
}

// MonitorAt возвращает номер монитора, на котором точка, или -1.
func MonitorAt(mons []hostprofile.WinMonitor, x, y int) int {
	for i, m := range mons {
		if x >= m.X && x < m.X+m.Width && y >= m.Y && y < m.Y+m.Height {
			return i
		}
	}
	return -1
}
