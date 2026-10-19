//go:build linux

package main

import (
	"testing"

	"lolssd/efi"
)

// Записи прошивки этой машины должны разбираться без паники.
func TestParseRealEntries(t *testing.T) {
	entries, err := bootEntries()
	if err != nil {
		t.Skip(err)
	}
	for num, opt := range entries {
		desc, guid := efi.ParseLoadOption(opt)
		t.Logf("Boot%04X %q %x", num, desc, guid)
	}
}
