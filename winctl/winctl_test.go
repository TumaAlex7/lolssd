package winctl

import (
	"slices"
	"testing"

	"lolssd/hostprofile"
)

func TestViewerArgs(t *testing.T) {
	got := ViewerArgs(hostprofile.WinMonitor{X: -2560, Y: 0, Width: 2560, Height: 1080}, 1)
	if !slices.Contains(got, "-geometry=640x480+-1920+270") || got[len(got)-1] != "127.0.0.1::5902" {
		t.Fatal(got)
	}
}

func TestMonitorAt(t *testing.T) {
	mons := []hostprofile.WinMonitor{{X: 0, Y: 0, Width: 2880, Height: 1620}, {X: 2880, Y: 0, Width: 2560, Height: 1080}}
	if MonitorAt(mons, 100, 100) != 0 || MonitorAt(mons, 2880, 0) != 1 || MonitorAt(mons, 2880, 1200) != -1 {
		t.Fatal()
	}
}
