package winctl

import (
	"encoding/base64"
	"strings"
	"testing"

	"lolssd/hostprofile"
)

func TestVMUdevRules(t *testing.T) {
	// Проверено в QEMU/KVM на Hyprland 0.56: центр второго выхода — (3199, 540).
	mons := []hostprofile.WinMonitor{{Width: 1920, Height: 1080}, {X: 1920, Width: 2560, Height: 1080}}
	got := VMUdevRules(mons)
	for _, want := range []string{
		`ATTRS{serial}=="lolssd-head0", ENV{LIBINPUT_CALIBRATION_MATRIX}="0.428571 0 0.000000 0 1.000000 0"`,
		`ATTRS{serial}=="lolssd-head1", ENV{LIBINPUT_CALIBRATION_MATRIX}="0.571429 0 0.428571 0 1.000000 0"`,
		// Проверено на стенде: Win+Shift+F24 от VNC → keyd laptop.conf → RIGHTMETA.
		`ATTRS{name}=="AT Translated Set 2 keyboard", ENV{KEYBOARD_KEY_76}="f23"` + "\n",
		`ENV{KEYBOARD_KEY_76}=="f23", IMPORT{builtin}="keyboard"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в\n%s", want, got)
		}
	}
}

func TestHDAArgs(t *testing.T) {
	got := strings.Join(hdaArgs("0000:03:00.6"), " ")
	for _, want := range []string{
		"pcie-root-port,id=rp1,chassis=1,bus=pcie.0,addr=0x18.0,multifunction=on",
		"pcie-root-port,id=rp3,chassis=3,bus=pcie.0,addr=0x18.2",
		"pci-testdev,bus=rp3,addr=0.0,multifunction=on",
		"ich9-intel-hda,id=hda,bus=rp3,addr=0.6",
		"hda-duplex,bus=hda.0,audiodev=snd0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q в %s", want, got)
		}
	}
	if strings.Contains(got, "rp4") {
		t.Errorf("лишний порт: %s", got)
	}
	if got := strings.Join(hdaArgs(""), " "); got != "-device ich9-intel-hda,id=hda -device hda-duplex,bus=hda.0,audiodev=snd0" {
		t.Errorf("без адреса: %s", got)
	}
	if got := strings.Join(hdaArgs("0000:00:1f.3"), " "); strings.Contains(got, "root-port") {
		t.Errorf("корневая шина: %s", got)
	}
}

func TestQEMUArgs(t *testing.T) {
	v := VM{Kernel: `C:\k`, Initrd: `C:\i`, Cmdline: "root=UUID=x rw", MemMiB: 8192, CPUs: 10,
		Disks:    []VMDisk{{Path: `\\.\PhysicalDrive1`, Model: "SAMSUNG MZVL8512HELU-00BTW", Serial: "S7J1NX0X598346"}},
		Monitors: []hostprofile.WinMonitor{{Width: 2560, Height: 1600}, {X: 2560, Width: 1920, Height: 1080}},
		AudioPCI: "0000:03:00.6", SerialLog: `C:\s.log`, QMPPort: 4444}
	a := QEMUArgs(v)
	s := strings.Join(a, "\x00")
	for _, want := range []string{
		"-accel\x00whpx\x00",
		"-cpu\x00max,vmx=off,svm=off",
		"if=none,id=disk0,format=raw,file=\\\\.\\PhysicalDrive1",
		"nvme,drive=disk0,serial=S7J1NX0X598346,model=SAMSUNG MZVL8512HELU-00BTW",
		"virtio-gpu-pci,id=video0,max_outputs=2,xres=2560,yres=1600",
		"q35,vmport=off",
		"127.0.0.1:1,id=vnc0,display=video0,head=0",
		"127.0.0.1:2,id=vnc1,display=video0,head=1",
		"usb-tablet,bus=xhci.0,serial=lolssd-head1,display=video0,head=1",
		"-append\x00root=UUID=x rw\x00",
		"tcp:127.0.0.1:4444,server=on,wait=off",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("нет %q", want)
		}
	}
	for i, x := range a {
		if x == "-smbios" {
			b64 := strings.TrimPrefix(a[i+1], "type=11,value=io.systemd.credential.binary:udev.rules.70-lolssd-vm=")
			rules, err := base64.StdEncoding.DecodeString(b64)
			if err != nil || string(rules) != VMUdevRules(v.Monitors) {
				t.Errorf("smbios: %v %q", err, rules)
			}
		}
	}
	v.IrqchipOff = true
	if !strings.Contains(strings.Join(QEMUArgs(v), " "), "-accel whpx,kernel-irqchip=off") {
		t.Error("нет kernel-irqchip=off")
	}
}

func TestKeydArgs(t *testing.T) {
	if keydArgs("") != nil {
		t.Error("без конфига keyd не трогаем")
	}
	a := keydArgs("[ids]\n*\n")
	dec := func(s, prefix string) string {
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tf := dec(a[1], "type=11,value=io.systemd.credential.binary:tmpfiles.extra=")
	if !strings.HasPrefix(tf, "f~ /run/lolssd-vm/keyd/vm.conf 0644 root root - ") ||
		dec(strings.TrimSpace(strings.Fields(tf)[6]), "") != "[ids]\n*\n" {
		t.Errorf("tmpfiles: %q", tf)
	}
	if d := dec(a[3], "type=11,value=io.systemd.credential.binary:systemd.unit-dropin.keyd.service="); d != "[Service]\nBindReadOnlyPaths=/run/lolssd-vm/keyd:/etc/keyd\n" {
		t.Errorf("drop-in: %q", d)
	}
}
