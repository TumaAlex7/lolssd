package keymap

import (
	"encoding/binary"
	"testing"
)

func TestScancodeMap(t *testing.T) {
	b := ScancodeMap(map[uint16]uint16{0x3a: 0x01})
	le := binary.LittleEndian
	if len(b) != 20 || le.Uint32(b[8:]) != 2 || le.Uint16(b[12:]) != 0x01 || le.Uint16(b[14:]) != 0x3a || le.Uint32(b[16:]) != 0 {
		t.Fatalf("% x", b)
	}
}

// Раскладка с дописанными клавишами компилируется, F23 становится простой клавишей.
func TestVMConfig(t *testing.T) {
	got := VMConfig(map[string]string{
		"default.conf": "[ids]\n*\n-k:0001:0001\n\n[main]\nmenu = rightmeta\ncompose = rightmeta\n",
		"laptop.conf":  "[ids]\nk:0001:0001\n\n[main]\nf23+leftshift+leftmeta = rightmeta\ncompose = menu\n",
	})
	want := "# lolssd: конфиги keyd хоста, сведённые для ВМ (все клавиатуры в ней — одна)\n[ids]\n*\n\n" +
		"[main]\nf23+leftshift+leftmeta = rightmeta\ncompose = rightmeta\nmenu = rightmeta\n"
	if got != want {
		t.Errorf("получено:\n%s\nожидалось:\n%s", got, want)
	}
	if VMConfig(nil) != "" {
		t.Error("без конфигов — пусто")
	}
}
