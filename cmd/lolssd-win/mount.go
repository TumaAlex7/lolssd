//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"lolssd/efi"
	"lolssd/hostprofile"
)

// Пусковой дистрибутив читает выбранный профиль отсюда (в WSL — /mnt/c/ProgramData/lolssd/).
var activeProfile = filepath.Join(os.Getenv("ProgramData"), "lolssd", "active-profile.json")

var monitorsPath = filepath.Join(os.Getenv("ProgramData"), "lolssd", "monitors.json")

// wslConfigMark отличает наш .wslconfig от написанного пользователем: чужой не трогаем.
const wslConfigMark = "# lolssd: generated, delete this line to keep your own settings"

// writeWSLConfig: половина памяти Windows под WSL (но не меньше 4 ГиБ) и возврат
// неиспользуемой памяти Windows — на 16 ГБ это важнее всего.
func writeWSLConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".wslconfig")
	if b, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(b), wslConfigMark) {
		log.Println("свой .wslconfig пользователя, оставляю как есть")
		return nil
	}
	mem := max(totalMemoryMiB()/2/1024, 4)
	cfg := fmt.Sprintf("%s\r\n[wsl2]\r\nmemory=%dGB\r\nswap=4GB\r\nguiApplications=true\r\n\r\n[experimental]\r\nautoMemoryReclaim=gradual\r\n", wslConfigMark, mem)
	return os.WriteFile(path, []byte(cfg), 0o644)
}

// mountHost готовит режим «Linux внутри Windows» (docs/ARCHITECTURE.md §6):
// находит на разделе LOLSSD профиль этого компьютера, подключает диски хоста в WSL целиком
// (--bare: разделы, LUKS и LVM поднимает уже пусковой дистрибутив) и сохраняет профиль для него.
// Профиля для этого компьютера нет — значит, пользователь этот режим не включал: молча выходим.
// Запускается при входе пользователя, с повышенными правами (так требует wsl --mount).
func mountHost() int {
	os.Remove(activeProfile)

	vol, err := lolssdVolume()
	if err != nil {
		log.Println(err)
		return 1
	}
	files, _ := filepath.Glob(filepath.Join(vol, "profiles", "*.json"))
	if len(files) == 0 {
		log.Println("профилей нет, режим Linux внутри Windows выключен")
		return 0
	}

	// PARTUUID → номер диска для всех дисков, кроме системного (этого SSD).
	sys, err := systemDisk()
	if err != nil {
		log.Println(err)
		return 1
	}
	where := map[string]int{}
	for n := 0; n < 64; n++ {
		if n == sys {
			continue
		}
		parts, err := diskGPT(n)
		if err != nil {
			continue // диска с таким номером нет или на нём не GPT
		}
		for _, p := range parts {
			where[efi.GUIDString(p.GUID)] = n
		}
	}

	// Подходит профиль, все разделы которого есть на этом компьютере; из нескольких — самый свежий.
	var best hostprofile.Profile
	var bestRaw []byte
	for _, f := range files {
		raw, err := os.ReadFile(f)
		var p hostprofile.Profile
		if err == nil {
			err = json.Unmarshal(raw, &p)
		}
		if err != nil {
			log.Printf("%s: %v", f, err)
			continue
		}
		uuids := p.PartUUIDs()
		found := len(uuids) > 0
		for _, u := range uuids {
			if _, ok := where[u]; !ok {
				found = false
			}
		}
		if found && p.Created.After(best.Created) {
			best, bestRaw = p, raw
		}
	}
	if bestRaw == nil {
		log.Println("профиля для этого компьютера нет, режим Linux внутри Windows выключен")
		return 0
	}
	log.Printf("профиль %s (%s, %s)", best.MachineID, best.Distro.ID, best.User.Name)

	// .wslconfig читается при старте ВМ WSL, а её запускает уже первый wsl --mount.
	if err := writeWSLConfig(); err != nil {
		log.Println(".wslconfig:", err)
	}
	mons, err := activeMonitors()
	if err == nil {
		var b []byte
		if b, err = json.MarshalIndent(mons, "", "  "); err == nil {
			err = os.WriteFile(monitorsPath, b, 0o644)
		}
	}
	if err != nil {
		log.Println("мониторы:", err)
	}

	disks := map[int]bool{}
	for _, u := range best.PartUUIDs() {
		disks[where[u]] = true
	}
	failed := 0
	for n := range disks {
		dev := fmt.Sprintf(`\\.\PHYSICALDRIVE%d`, n)
		cmd := exec.Command("wsl.exe", "--mount", dev, "--bare")
		cmd.Env = append(os.Environ(), "WSL_UTF8=1") // иначе wsl.exe пишет в UTF-16
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(string(out))
		// Повторный запуск (диск уже подключён) — не ошибка.
		if err != nil && !strings.Contains(msg, "ALREADY") {
			log.Printf("wsl --mount %s: %v: %s", dev, err, msg)
			failed++
			continue
		}
		log.Printf("подключён %s", dev)
	}
	if failed > 0 {
		return 1
	}

	if err := os.WriteFile(activeProfile, bestRaw, 0o644); err != nil {
		log.Println("запись активного профиля:", err)
		return 1
	}
	return 0
}

// lolssdVolume находит том раздела LOLSSD по метке ФС и возвращает его корень, например `D:\`.
func lolssdVolume() (string, error) {
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		label := make([]uint16, windows.MAX_PATH+1)
		if windows.GetVolumeInformation(windows.StringToUTF16Ptr(root), &label[0], uint32(len(label)), nil, nil, nil, nil, 0) != nil {
			continue
		}
		if windows.UTF16ToString(label) == "LOLSSD" {
			return root, nil
		}
	}
	return "", fmt.Errorf("том с меткой LOLSSD не найден")
}
