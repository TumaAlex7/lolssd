//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDrivers(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	for _, f := range []string{"wifi/netwtw.inf", "wifi/netwtw.sys", "wifi/x64/netwtw.cat", "Setup.EXE", "audio/realtek.msi"} {
		os.MkdirAll(filepath.Join(src, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(src, f), []byte(f), 0o644)
	}
	infs, setups, err := copyDrivers(src, filepath.Join(dst, "pack"))
	if err != nil || infs != 1 || setups != 2 {
		t.Fatalf("infs=%d setups=%d err=%v", infs, setups, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "pack/wifi/x64/netwtw.cat")); string(b) != "wifi/x64/netwtw.cat" {
		t.Fatalf("nested file not copied: %q", b)
	}
	// Один файл: кладётся под своим именем.
	infs, setups, err = copyDrivers(filepath.Join(src, "Setup.EXE"), filepath.Join(dst, "Setup.EXE"))
	if err != nil || setups != 1 {
		t.Fatalf("single file: setups=%d err=%v", setups, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "Setup.EXE")); err != nil {
		t.Fatal(err)
	}
}
