package keymap

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Конфиги keyd автора: внешние клавиатуры (Menu/Compose → правая Meta) и ноутбук
// (Copilot = LMeta+LShift+F23 → правая Meta).
func authorPlan(t *testing.T) Plan {
	files := map[string]string{}
	for _, f := range []string{"default.conf", "laptop.conf"} {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		files[f] = string(b)
	}
	return Build(ParseKeyd(files))
}

func TestAuthorPlan(t *testing.T) {
	p := authorPlan(t)
	want := map[uint16]uint16{
		0xe05d: 0xe05c, // Compose (Menu на PC) → правая Meta, то есть правая Win
	}
	if _, ok := p.Scancodes[0xe05b]; ok {
		t.Fatal("Win переназначена, а должна остаться Win:", p.Scancodes)
	}
	for from, to := range want {
		if p.Scancodes[from] != to {
			t.Errorf("Scancode %04x → %04x, ожидалось %04x", from, p.Scancodes[from], to)
		}
	}
	if len(p.Chords) != 1 {
		t.Fatalf("сочетания: %+v", p.Chords)
	}
	c := p.Chords[0]
	if c.Trigger != "F23" || c.Target != "Super_R" || c.TMod != "SUPER" ||
		strings.Join(c.Mods, ",") != "SHIFT,SUPER" || len(c.Hide) != 2 {
		t.Fatalf("%+v", c)
	}
	if p.XKB["FK23"] != "F23" {
		t.Fatal(p.XKB)
	}
	// «menu» (KEY_MENU) в Windows нет, а конфликт Compose из laptop.conf решён в пользу «*».
	notes := strings.Join(p.Notes, "\n")
	if !strings.Contains(notes, "menu = rightmeta") || !strings.Contains(notes, "compose = menu") {
		t.Fatal(notes)
	}
}

func TestScancodeMap(t *testing.T) {
	b := ScancodeMap(map[uint16]uint16{0x3a: 0x01})
	le := binary.LittleEndian
	if len(b) != 20 || le.Uint32(b[8:]) != 2 || le.Uint16(b[12:]) != 0x01 || le.Uint16(b[14:]) != 0x3a || le.Uint32(b[16:]) != 0 {
		t.Fatalf("% x", b)
	}
}

// Раскладка с дописанными клавишами компилируется, F23 становится простой клавишей.
func TestPatchXKB(t *testing.T) {
	if _, err := exec.LookPath("xkbcli"); err != nil {
		t.Skip("нет xkbcli")
	}
	out, err := exec.Command("xkbcli", "compile-keymap", "--layout", "us,ru", "--options", "grp:alt_shift_toggle,altwin:menu").Output()
	if err != nil {
		t.Fatal(err)
	}
	patched, err := PatchXKB(string(out), authorPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "k.xkb")
	os.WriteFile(f, []byte(patched), 0o644)
	km, _ := exec.Command("xkbcli", "compile-keymap", "--from-xkb", f).Output()
	block := string(km)[strings.Index(string(km), "key <FK23>"):]
	block = block[:strings.Index(block, "};")]
	if !regexp.MustCompile(`ONE_LEVEL[\s\S]*\[\s*F23\s*\]`).MatchString(block) {
		t.Fatalf("F23 не переопределена:\n%s", block)
	}
}

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
