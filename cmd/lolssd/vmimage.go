package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lolssd/hostprofile"
)

// Диски ВМ — NVMe (QEMU): драйвер в initramfs нужен, даже если autodetect на этом железе его не взял.
var vmModules = []string{"nvme"}

// buildVMImage собирает образ загрузки системы хоста в ВМ (QEMU в Windows) — UKI на разделе
// LOLSSD, — её же mkinitcpio и ядром. В системе ничего не меняется: свой конфиг mkinitcpio
// (конфиг системы плюс vmModules; autodetect оставлен) и своя командная строка:
//   - корень по UUID: имена дисков в ВМ другие;
//   - остальное — из текущей командной строки (LUKS, resume и т. п.), кроме initrd= и root=;
//   - консоль ещё и на ttyS0: COM-порт ВМ читается из Windows, это журнал загрузки без экрана.
func buildVMImage(dir string) (*hostprofile.VMImage, error) {
	if _, err := exec.LookPath("mkinitcpio"); err != nil {
		return nil, fmt.Errorf("нет mkinitcpio (пока поддерживаются системы с ним, как Arch)")
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return nil, err
	}
	kernel := strings.TrimSpace(string(b))
	vmlinuz := filepath.Join("/usr/lib/modules", kernel, "vmlinuz")
	if _, err := os.Stat(vmlinuz); err != nil {
		return nil, fmt.Errorf("нет %s: %w", vmlinuz, err)
	}
	uuid, err := exec.Command("findmnt", "-no", "UUID", "/").Output()
	if err != nil || strings.TrimSpace(string(uuid)) == "" {
		return nil, fmt.Errorf("UUID корневой ФС не найден")
	}
	cmdline := []string{"root=UUID=" + strings.TrimSpace(string(uuid))}
	cur, _ := os.ReadFile("/proc/cmdline")
	for _, a := range strings.Fields(string(cur)) {
		switch {
		case strings.HasPrefix(a, "initrd="), strings.HasPrefix(a, "root="), strings.HasPrefix(a, "BOOT_IMAGE="),
			strings.HasPrefix(a, "console="):
			continue
		}
		cmdline = append(cmdline, a)
	}
	cmdline = append(cmdline, "console=tty0", "console=ttyS0,115200")

	tmp, err := os.MkdirTemp("", "lolssd-vm")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	conf := filepath.Join(tmp, "mkinitcpio.conf")
	confText := ". /etc/mkinitcpio.conf\nMODULES+=(" + strings.Join(vmModules, " ") + ")\n"
	cmdFile := filepath.Join(tmp, "cmdline")
	if err := os.WriteFile(conf, []byte(confText), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cmdFile, []byte(strings.Join(cmdline, " ")+"\n"), 0o644); err != nil {
		return nil, err
	}

	rel := filepath.Join("vm", strings.TrimSpace(readFile("/etc/machine-id"))+".efi")
	out := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	fmt.Println("Собираю образ загрузки для ВМ (ядро", kernel+")…")
	cmd := exec.Command("mkinitcpio", "-c", conf, "-k", kernel, "-U", out+".part",
		"--cmdline", cmdFile, "--kernelimage", vmlinuz)
	if b, err := cmd.CombinedOutput(); err != nil {
		os.Remove(out + ".part")
		return nil, fmt.Errorf("mkinitcpio: %v\n%s", err, b)
	}
	if err := os.Rename(out+".part", out); err != nil {
		return nil, err
	}
	return &hostprofile.VMImage{Path: rel, Kernel: kernel, Cmdline: strings.Join(cmdline, " ")}, nil
}
