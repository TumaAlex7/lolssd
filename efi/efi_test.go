package efi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOptionRoundTrip(t *testing.T) {
	p := Part{Num: 1, First: 2048, Last: 534527, GUID: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}
	desc, guid := ParseLoadOption(BuildLoadOption("lolssd", `\EFI\BOOT\BOOTX64.EFI`, p))
	if desc != "lolssd" || guid != p.GUID {
		t.Fatalf("got %q %x", desc, guid)
	}
}

func TestBootOrderRoundTrip(t *testing.T) {
	order := []uint16{1, 0, 0x2a}
	got := ParseBootOrder(BuildBootOrder(order))
	if len(got) != 3 || got[0] != 1 || got[2] != 0x2a {
		t.Fatal(got)
	}
}

func TestReadGPT(t *testing.T) {
	if _, err := exec.LookPath("sfdisk"); err != nil {
		t.Skip("нет sfdisk")
	}
	for _, sector := range []string{"512", "4096"} {
		img := filepath.Join(t.TempDir(), "disk.img")
		if err := os.WriteFile(img, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(img, 128<<20); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sfdisk", "--quiet", "--sector-size", sector, img)
		cmd.Stdin = strings.NewReader("label: gpt\nsize=64MiB, type=uefi, name=ESP\nsize=32MiB, name=LOLSSD\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}

		f, err := os.Open(img)
		if err != nil {
			t.Fatal(err)
		}
		parts, err := ReadGPT(f)
		f.Close()
		if err != nil {
			t.Fatal(sector, err)
		}
		if len(parts) != 2 || parts[0].Type != ESPType || parts[1].Name != "LOLSSD" {
			t.Fatalf("%s: %+v", sector, parts)
		}
	}
}

func TestStrip(t *testing.T) {
	ssd := Part{Num: 1, First: 2048, Last: 4095, GUID: [16]byte{1}}
	host := Part{Num: 1, First: 2048, Last: 4095, GUID: [16]byte{2}}
	entries := map[uint16][]byte{
		0: BuildLoadOption("Linux", `\EFI\systemd\systemd-bootx64.efi`, host),
		1: BuildLoadOption("Windows Boot Manager", `\EFI\Microsoft\Boot\bootmgfw.efi`, ssd),
		2: BuildLoadOption("lolssd", `\EFI\BOOT\BOOTX64.EFI`, ssd),
		3: {0, 0}, // мусор не должен ломать разбор
	}
	order, remove := Strip([]uint16{1, 0, 2, 3}, entries, ssd.GUID, "lolssd")
	if len(order) != 2 || order[0] != 0 || order[1] != 3 || len(remove) != 1 || remove[0] != 1 {
		t.Fatal(order, remove)
	}
}
