package winctl

import (
	"slices"
	"testing"

	"lolssd/hostprofile"
)

func TestAssign(t *testing.T) {
	titles := map[uintptr]string{10: "aquamarine", 20: "aquamarine", 30: "aquamarine"}
	slots := Assign(nil, []uintptr{10}, titles)
	slots = Assign(slots, []uintptr{10, 20}, titles)
	slots = Assign(slots, []uintptr{20, 10, 30}, titles) // порядок перечисления не важен
	if !slices.Equal(slots, []uintptr{10, 20, 30}) {
		t.Fatal(slots)
	}
	// Окно 20 закрылось — место освобождается, остальные не сдвигаются.
	slots = Assign(slots, []uintptr{10, 30}, titles)
	if !slices.Equal(slots, []uintptr{10, 0, 30}) {
		t.Fatal(slots)
	}
	// Номер в заголовке важнее порядка появления.
	titles[40], titles[50] = "aquamarine", "WAYLAND-1 (lolssd)"
	slots = Assign(nil, []uintptr{40, 50}, titles)
	if !slices.Equal(slots, []uintptr{50, 40}) {
		t.Fatal(slots)
	}
}

func TestMonitorAt(t *testing.T) {
	mons := []hostprofile.WinMonitor{{X: 0, Y: 0, Width: 2880, Height: 1620}, {X: 2880, Y: 0, Width: 2560, Height: 1080}}
	if MonitorAt(mons, 100, 100) != 0 || MonitorAt(mons, 2880, 0) != 1 || MonitorAt(mons, 2880, 1200) != -1 {
		t.Fatal()
	}
}
