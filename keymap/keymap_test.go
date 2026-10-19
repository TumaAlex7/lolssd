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
		0xe05b: 0x64, // левая Win → F13
		0xe05c: 0x65, // правая Win → F14
		0xe05d: 0x65, // Compose (Menu на PC) → правая Meta, то есть F14
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
	if p.XKB["FK13"] != "Super_L" || p.XKB["FK14"] != "Super_R" || p.XKB["FK23"] != "F23" {
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

// Раскладка с дописанными клавишами компилируется, F13/F14 — модификатор Super.
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
	mm, err := exec.Command("xkbcli", "compile-keymap", "--from-xkb", f, "--modmaps").CombinedOutput()
	if err != nil {
		t.Fatal(err, string(mm))
	}
	s := string(mm)
	for _, k := range []string{"FK13", "FK14"} {
		i := strings.Index(s, "  "+k+":")
		if i < 0 || !strings.Contains(s[i:i+60], "Mod4") {
			t.Fatalf("%s не модификатор Super:\n%s", k, s[max(i, 0):max(i, 0)+60])
		}
	}
	km, _ := exec.Command("xkbcli", "compile-keymap", "--from-xkb", f).Output()
	block := string(km)[strings.Index(string(km), "key <FK23>"):]
	block = block[:strings.Index(block, "};")]
	if !regexp.MustCompile(`ONE_LEVEL[\s\S]*\[\s*F23\s*\]`).MatchString(block) {
		t.Fatalf("F23 не переопределена:\n%s", block)
	}
}
