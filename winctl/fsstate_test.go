package winctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Образы делают настоящие mkfs.ext4, debugfs и mkswap (e2fsprogs, util-linux); нет их — пропуск.
func image(t *testing.T, size int64, cmds ...[]string) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "img")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, size); err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if _, err := exec.LookPath(c[0]); err != nil {
			t.Skip(c[0], "нет")
		}
		if out, err := exec.Command(c[0], append(c[1:], p)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", c, err, out)
		}
	}
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestPartitionState(t *testing.T) {
	ext4 := []string{"mkfs.ext4", "-q", "-F"}
	if s := PartitionState(image(t, 64<<20, ext4), 0); s != FSClean {
		t.Errorf("чистый ext4: %v", s)
	}
	dirty := []string{"debugfs", "-w", "-R", "feature needs_recovery"}
	if s := PartitionState(image(t, 64<<20, ext4, dirty), 0); s != FSUnclean {
		t.Errorf("ext4 с незакрытым журналом: %v", s)
	}
	swap := image(t, 64<<20, []string{"mkswap", "-q"})
	if s := PartitionState(swap, 0); s != FSClean {
		t.Errorf("swap: %v", s)
	}
	// Так подпись меняет ядро, когда пишет в раздел образ гибернации (swsusp).
	if _, err := swap.WriteAt([]byte("S1SUSPEND\x00"), 4086); err != nil {
		t.Fatal(err)
	}
	if s := PartitionState(swap, 0); s != FSHibernated {
		t.Errorf("swap с образом гибернации: %v", s)
	}
	if s := PartitionState(image(t, 64<<20), 0); s != FSUnknown {
		t.Errorf("пустой раздел: %v", s)
	}
}
