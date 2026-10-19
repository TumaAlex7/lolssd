//go:build linux

package main

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSwapHibernated(t *testing.T) {
	page := make([]byte, 4096)
	copy(page[4086:], "SWAPSPACE2")
	if swapHibernated(page) {
		t.Fatal("обычный swap принят за гибернацию")
	}
	copy(page[4086:], "S1SUSPEND\x00")
	if !swapHibernated(page) {
		t.Fatal("образ гибернации не замечен")
	}
}

func TestExt4Problem(t *testing.T) {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("нет mkfs.ext4")
	}
	img := filepath.Join(t.TempDir(), "fs.img")
	if out, err := exec.Command("mkfs.ext4", "-q", "-F", img, "16M").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	f, err := os.Open(img)
	if err != nil {
		t.Fatal(err)
	}
	sb := make([]byte, 1024)
	_, err = f.ReadAt(sb, 1024)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}

	if msg := ext4Problem(sb, "/"); msg != "" {
		t.Fatal("свежая ФС считается грязной:", msg)
	}
	binary.LittleEndian.PutUint32(sb[0x60:], binary.LittleEndian.Uint32(sb[0x60:])|0x4)
	if ext4Problem(sb, "/") == "" {
		t.Fatal("needs_recovery не замечен")
	}
}
