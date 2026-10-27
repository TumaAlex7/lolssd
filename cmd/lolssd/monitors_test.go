//go:build linux

package main

import (
	"os/exec"
	"testing"
)

// На машине с Hyprland: все включённые мониторы попадают в профиль с EDID.
func TestHostMonitors(t *testing.T) {
	if exec.Command("hyprctl", "monitors").Run() != nil {
		t.Skip("нет Hyprland")
	}
	ms := hostMonitors()
	if len(ms) == 0 {
		t.Fatal("мониторов нет")
	}
	for _, m := range ms {
		if len(m.EDID) != 128 || m.Width == 0 || m.Scale == 0 {
			t.Errorf("%+v", m)
		}
		t.Logf("%s: %d,%d %dx%d ×%.2f mirror=%q off=%v edid=%x", m.Name, m.X, m.Y, m.Width, m.Height, m.Scale, m.MirrorOf, m.Disabled, m.EDID[8:16])
	}
}
