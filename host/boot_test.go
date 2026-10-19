package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOptionRoundTrip(t *testing.T) {
	esp := gptPart{num: 1, first: 2048, last: 534527, guid: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}}
	desc, guid := parseLoadOption(buildLoadOption(esp))
	if desc != bootLabel || guid != esp.guid {
		t.Fatalf("got %q %x", desc, guid)
	}
}

// Записи прошивки этой машины должны разбираться без паники.
func TestParseRealEntries(t *testing.T) {
	entries, err := bootEntries()
	if err != nil {
		t.Skip(err)
	}
	for num, opt := range entries {
		desc, guid := parseLoadOption(opt)
		t.Logf("Boot%04X %q %x", num, desc, guid)
	}
}

func TestReadGPT(t *testing.T) {
	if _, err := exec.LookPath("sfdisk"); err != nil {
		t.Skip("нет sfdisk")
	}
	img := filepath.Join(t.TempDir(), "disk.img")
	if err := os.Truncate(img, 0); err != nil {
		os.WriteFile(img, nil, 0o644)
	}
	if err := os.Truncate(img, 128<<20); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sfdisk", "--quiet", img)
	cmd.Stdin = strings.NewReader("label: gpt\nsize=64MiB, type=uefi, name=ESP\nsize=32MiB, name=" + partLabel + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}

	parts, err := readGPT(img, 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].typ != espType || parts[1].name != partLabel || parts[0].first != 2048 {
		t.Fatalf("%+v", parts)
	}
}
