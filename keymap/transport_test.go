package keymap

import (
	"os"
	"strings"
	"testing"
)

// Без профиля переназначений нет, и в lolssd.reg Scancode Map быть не должно: иначе
// lolssd-win mount (который тогда значение удаляет) и образ разойдутся во мнении, что действует.
func TestRegNoScancodeMap(t *testing.T) {
	b, err := os.ReadFile("../build/setup/lolssd.reg")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(Build(nil, nil).Scancodes); n != 0 {
		t.Fatalf("без профиля %d переназначений", n)
	}
	if strings.Contains(string(b), `"Scancode Map"=`) {
		t.Fatal("в lolssd.reg задан Scancode Map")
	}
}
