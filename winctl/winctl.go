// Package winctl — логика контроллера окон Windows без вызовов ОС, чтобы её можно было
// проверять тестами на Linux. Сам контроллер — cmd/lolssd-win/controller.go.
package winctl

import (
	"regexp"
	"strconv"
	"strings"

	"lolssd/hostprofile"
)

var outputRe = regexp.MustCompile(`WAYLAND-(\d+)`)

// IsOutputWindow: окно вложенного выхода Hyprland — окно WSLg с заголовком бэкенда aquamarine
// или с именем выхода в заголовке. [проверить] точный заголовок на живой системе.
func IsOutputWindow(title string) bool {
	return strings.HasPrefix(title, "aquamarine") || outputRe.MatchString(title)
}

// SlotFromTitle возвращает номер выхода (с нуля), если он виден в заголовке, иначе -1.
func SlotFromTitle(title string) int {
	m := outputRe.FindStringSubmatch(title)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n - 1
}

// Assign раскладывает окна по номерам выходов. slots — текущее состояние (окно или 0),
// alive — окна, найденные сейчас, в порядке обнаружения. Окна с номером в заголовке встают
// на свой номер, остальные — на первые свободные места в порядке появления: выходы
// создаются по одному, так что порядок появления совпадает с нумерацией.
func Assign(slots []uintptr, alive []uintptr, titles map[uintptr]string) []uintptr {
	live := map[uintptr]bool{}
	for _, h := range alive {
		live[h] = true
	}
	known := map[uintptr]bool{}
	for i, h := range slots {
		if h != 0 && !live[h] {
			slots[i] = 0
		}
		if slots[i] != 0 {
			known[slots[i]] = true
		}
	}
	put := func(i int, h uintptr) {
		for len(slots) <= i {
			slots = append(slots, 0)
		}
		slots[i] = h
		known[h] = true
	}
	for _, h := range alive {
		if n := SlotFromTitle(titles[h]); !known[h] && n >= 0 && (n >= len(slots) || slots[n] == 0) {
			put(n, h)
		}
	}
	for _, h := range alive {
		if known[h] {
			continue
		}
		i := 0
		for i < len(slots) && slots[i] != 0 {
			i++
		}
		put(i, h)
	}
	return slots
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
