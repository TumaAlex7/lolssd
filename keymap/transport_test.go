package keymap

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Значение Scancode Map в lolssd.reg должно совпадать с транспортом Meta без профиля:
// иначе lolssd-win mount и образ разойдутся во мнении, что действует.
func TestRegTransport(t *testing.T) {
	b, err := os.ReadFile("../build/setup/lolssd.reg")
	if err != nil {
		t.Fatal(err)
	}
	var hex []string
	for _, x := range ScancodeMap(Build(nil, nil).Scancodes) {
		hex = append(hex, fmt.Sprintf("%02x", x))
	}
	if want := `"Scancode Map"=hex:` + strings.Join(hex, ","); !strings.Contains(string(b), want) {
		t.Fatalf("в lolssd.reg нет\n%s", want)
	}
}
