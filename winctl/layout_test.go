package winctl

import (
	"reflect"
	"testing"

	"lolssd/hostprofile"
)

func edid(id byte) []byte {
	b := make([]byte, 128)
	copy(b, []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0})
	b[8], b[9], b[10], b[15] = 0x1e, 0x6d, id, id
	return b
}

// Раскладка автора: ноутбук (2880×1620, ×1.5) слева, ультраширокий LG посередине, LG IPS226
// справа; в Windows они на других портах и в другом порядке номеров.
func TestPlanAuthorLayout(t *testing.T) {
	host := []hostprofile.HostMonitor{
		{Name: "eDP-1", EDID: edid(1), X: 0, Y: 0, Width: 2880, Height: 1620, Scale: 1.5},
		{Name: "DP-8", EDID: edid(3), X: 4480, Y: 0, Width: 1920, Height: 1080, Scale: 1},
		{Name: "DP-9", EDID: edid(2), X: 1920, Y: 0, Width: 2560, Height: 1080, Scale: 1},
	}
	targets := map[int][]byte{10: edid(2), 11: edid(3), 12: edid(1)}
	groups, off := PlanDisplays(host, targets)
	var order []int
	for _, g := range groups {
		order = append(order, g.Targets...)
	}
	if !reflect.DeepEqual(order, []int{12, 10, 11}) || len(off) != 0 {
		t.Fatalf("порядок %v, выключены %v", order, off)
	}
	pos := PlaceDisplays(groups, map[int][2]int{12: {2880, 1620}, 10: {2560, 1080}, 11: {1920, 1080}})
	want := map[int][2]int{12: {0, 0}, 10: {2880, 0}, 11: {5440, 0}}
	if !reflect.DeepEqual(pos, want) {
		t.Fatalf("позиции %v, ждали %v", pos, want)
	}
}

func TestPlanMirrorOffUnknown(t *testing.T) {
	host := []hostprofile.HostMonitor{
		{Name: "eDP-1", EDID: edid(1), Width: 1920, Height: 1080, Scale: 1},
		{Name: "HDMI-A-1", EDID: edid(2), X: 1920, Width: 1920, Height: 1080, Scale: 1, MirrorOf: "eDP-1"},
		{Name: "DP-1", EDID: edid(3), X: 1920, Width: 2560, Height: 1440, Scale: 1, Disabled: true},
	}
	// 4 — монитор, которого в профиле нет (другой стол).
	targets := map[int][]byte{1: edid(2), 2: edid(1), 3: edid(3), 4: edid(9)}
	groups, off := PlanDisplays(host, targets)
	if len(groups) != 2 || !reflect.DeepEqual(groups[0].Targets, []int{2, 1}) ||
		groups[1].Host != nil || !reflect.DeepEqual(groups[1].Targets, []int{4}) || !reflect.DeepEqual(off, []int{3}) {
		t.Fatalf("группы %+v, выключены %v", groups, off)
	}
	pos := PlaceDisplays(groups, map[int][2]int{2: {1920, 1080}, 4: {1280, 1024}})
	want := map[int][2]int{2: {0, 0}, 1: {0, 0}, 4: {1920, 0}}
	if !reflect.DeepEqual(pos, want) {
		t.Fatalf("позиции %v", pos)
	}
}

// Монитор над ноутбуком со смещением и масштабом 2 у ноутбука: стыковка по вертикали.
func TestPlaceStacked(t *testing.T) {
	host := []hostprofile.HostMonitor{
		{Name: "DP-1", EDID: edid(1), X: 0, Y: 0, Width: 2560, Height: 1440, Scale: 1},
		{Name: "eDP-1", EDID: edid(2), X: 640, Y: 1440, Width: 2560, Height: 1600, Scale: 2},
	}
	groups, _ := PlanDisplays(host, map[int][]byte{1: edid(1), 2: edid(2)})
	pos := PlaceDisplays(groups, map[int][2]int{1: {2560, 1440}, 2: {2560, 1600}})
	if pos[1] != [2]int{0, 0} || pos[2][1] != 1440 {
		t.Fatalf("позиции %v", pos)
	}
}

// EDID узнаётся и когда хвост базового блока отличается (производитель, модель, серийный — те же).
func TestSameMonitorTail(t *testing.T) {
	a, b := edid(5), edid(5)
	b[127] ^= 0xff
	if !sameMonitor(a, b) || sameMonitor(a, edid(6)) {
		t.Fatal("сравнение EDID")
	}
}
