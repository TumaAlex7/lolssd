//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"lolssd/hostprofile"
)

// Рабочий стол хоста в WSL — по образцу hyprland-wsl (github.com/ItsOverk1ll/hyprland-wsl),
// код свой:
//   - в WSL нет устройства вывода, а для выделения буферов Hyprland'у нужно DRM-устройство:
//     его даёт vgem, виртуальное DRM-устройство из ядра WSL;
//   - штатный aquamarine этого не умеет — сеанс подкладывает пропатченный
//     (build/hypr/aquamarine-headless-device.patch), собранный под версию хоста;
//   - рисует GPU через d3d12 (/dev/dxg), буферы d3d12 «одалживают» имя vgem;
//   - картинку каждого выхода отдаёт wayvnc на 127.0.0.1:5901…, Windows показывает её
//     полноэкранным TigerVNC (lolssd-win desktop).

const (
	aqCache  = "/var/cache/lolssd/aquamarine" // в пусковом дистрибутиве, по версиям
	aqPatch  = "/usr/local/share/lolssd/aquamarine-headless-device.patch"
	xdgDir   = "/run/lolssd/xdg" // XDG_RUNTIME_DIR сеанса, общий с wayvnc
	vncPort0 = 5900              // выход N — порт 5900+N
	aqLibIn  = inner + "/aq/lib" // кэш внутри контейнера
)

// runHyprland: vgem, пропатченный aquamarine, Hyprland без экрана и wayvnc на каждый выход.
// Возвращается, когда Hyprland завершился.
func runHyprland(p hostprofile.Profile, wins []hostprofile.WinMonitor, n int, cfg string) int {
	card, err := vgemCard()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lib, err := ensureAquamarine(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Свой XDG_RUNTIME_DIR, короткий: путь к сокету Hyprland ограничен 108 байтами
	// (с /run/lolssd-wsl/wslg/runtime-dir/hypr/<подпись>/ он не влезал), и его видит wayvnc.
	os.RemoveAll(xdgDir)
	if err := os.MkdirAll(xdgDir, 0o700); err == nil {
		err = os.Chown(xdgDir, p.User.UID, p.User.GID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "XDG_RUNTIME_DIR:", err)
		return 1
	}
	userRun := fmt.Sprintf("/run/user/%d", p.User.UID)

	var modes []string
	for _, w := range wins {
		modes = append(modes, fmt.Sprintf("%dx%d", w.Width, w.Height))
	}
	if len(modes) == 0 {
		modes = []string{"1920x1080"}
	}

	extra := []string{
		"--bind-ro=" + sessionDir + ":" + inner + "/session",
		"--bind-ro=" + filepath.Dir(filepath.Dir(lib)) + ":" + inner + "/aq",
		"--bind=" + xdgDir + ":" + userRun,
		"--bind=" + card,
		"--property=DeviceAllow=char-drm rw",
	}
	// Без WAYLAND_DISPLAY aquamarine не пытается встать окном в WSLg и берёт headless-бэкенд.
	// Рисует d3d12 через /dev/dxg (его пробрасывает runIn); без него — llvmpipe на CPU.
	driver := "LP_NUM_THREADS=4"
	if _, err := os.Stat("/dev/dxg"); err == nil {
		driver = "GALLIUM_DRIVER=d3d12"
	}
	cmd := []string{"env", "-u", "WAYLAND_DISPLAY", "-u", "DISPLAY", driver,
		"XDG_RUNTIME_DIR=" + userRun,
		"AQ_HEADLESS_DRM_DEVICE=" + card,
		"AQ_HEADLESS_MODES=" + strings.Join(modes, ","),
		"LD_LIBRARY_PATH=" + aqLibIn + ":" + inner + "/lib/lib",
		"Hyprland", "--config", cfg}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveVNC(n, stop)
	}()
	fmt.Printf("Hyprland: %s на %s, выходов %d, VNC на 127.0.0.1:%d…%d\n", strings.Join(modes, ","), card, n, vncPort0+1, vncPort0+n)
	code := runIn(p, extra, cmd)
	close(stop)
	<-done
	return code
}

// vgemCard загружает vgem и возвращает его основной узел. Буферы выделяются через него:
// через render-узел vgem выделять не даёт.
func vgemCard() (string, error) {
	if out, err := exec.Command("modprobe", "vgem").CombinedOutput(); err != nil {
		return "", fmt.Errorf("modprobe vgem: %v: %s", err, out)
	}
	for range 20 {
		cards, _ := filepath.Glob("/sys/class/drm/card*")
		for _, c := range cards {
			if !regexp.MustCompile(`/card\d+$`).MatchString(c) {
				continue
			}
			if dev, _ := filepath.EvalSymlinks(filepath.Join(c, "device")); filepath.Base(dev) == "vgem" {
				return "/dev/dri/" + filepath.Base(c), nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("после modprobe vgem устройство vgem не появилось в /sys/class/drm")
}

// ensureAquamarine возвращает пропатченный libaquamarine под версию хоста. Собирается один раз
// на версию — внутри системы хоста, её же компилятором и заголовками (ABI совпадает с её
// Hyprland), — и хранится в пусковом дистрибутиве, на диск хоста ничего не пишется.
func ensureAquamarine(p hostprofile.Profile) (string, error) {
	libs, _ := filepath.Glob(filepath.Join(rootDir, "usr/lib/libaquamarine.so.*.*.*"))
	if len(libs) == 0 {
		return "", fmt.Errorf("в системе хоста нет libaquamarine (Hyprland не установлен?)")
	}
	base := filepath.Base(libs[0]) // libaquamarine.so.0.15.1
	version := strings.TrimPrefix(base, "libaquamarine.so.0.")
	if strings.HasPrefix(base, "libaquamarine.so.") && !strings.HasPrefix(base, "libaquamarine.so.0.") {
		version = strings.TrimPrefix(base, "libaquamarine.so.")
	}
	dir := filepath.Join(aqCache, version)
	lib := filepath.Join(dir, "lib", base)
	if _, err := os.Stat(lib); err == nil {
		return lib, nil
	}

	fmt.Printf("Собираю aquamarine %s с патчем под систему хоста (один раз на версию)…\n", version)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.Chown(dir, p.User.UID, p.User.GID)
	}
	patch, err := os.ReadFile(aqPatch)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "aquamarine.patch"), patch, 0o644)
	}
	if err != nil {
		return "", fmt.Errorf("подготовка сборки aquamarine: %w", err)
	}

	// Сборка в системе хоста от имени пользователя: нужны git, cmake, ninja и заголовки,
	// которые на Arch приходят вместе с Hyprland. Исходники — во временном каталоге контейнера.
	script := `set -e
v=$1; out=/run/lolssd-wsl/aqbuild
for t in git cmake ninja c++ pkg-config hyprwayland-scanner; do
	command -v $t >/dev/null || { echo "для сборки aquamarine в системе нужен $t"; exit 1; }
done
src=$(mktemp -d)
git clone -q --depth 1 --branch v$v https://github.com/hyprwm/aquamarine "$src"
cd "$src"
git apply "$out/aquamarine.patch" || { echo "патч не накладывается на aquamarine $v"; exit 1; }
cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Release >/dev/null
ninja -C build >/dev/null
mkdir -p "$out/lib"
cp -P build/libaquamarine.so* "$out/lib/"
rm -rf "$src"`
	code := runIn(p, []string{"--bind=" + dir + ":" + inner + "/aqbuild"},
		[]string{"sh", "-c", script, "sh", version})
	if _, err := os.Stat(lib); code != 0 || err != nil {
		return "", fmt.Errorf("сборка aquamarine %s не удалась (код %d)", version, code)
	}
	return lib, nil
}

// serveVNC ждёт сокет Hyprland и выходы HEADLESS-1…n и держит на каждом по wayvnc,
// пока не закроют stop. wayvnc работает в пусковом дистрибутиве и подключается к сокету
// Hyprland через общий XDG_RUNTIME_DIR.
func serveVNC(n int, stop chan struct{}) {
	var socket string
	for socket == "" {
		select {
		case <-stop:
			return
		case <-time.After(300 * time.Millisecond):
		}
		matches, _ := filepath.Glob(filepath.Join(xdgDir, "wayland-*"))
		for _, m := range matches {
			if !strings.HasSuffix(m, ".lock") {
				socket = filepath.Base(m)
			}
		}
	}

	procs := make([]*exec.Cmd, n)
	alive := make([]atomic.Bool, n)
	for {
		for i := range n {
			if alive[i].Load() {
				continue
			}
			// Выходы 2…n создаются обёрткой конфига при старте; пока выхода нет, wayvnc
			// завершится — пробуем снова на следующем круге. У каждого свой управляющий сокет:
			// общий по умолчанию (wayvncctl) второй экземпляр не откроет.
			c := exec.Command("wayvnc", "--output=HEADLESS-"+strconv.Itoa(i+1), "--max-fps=60",
				"--socket="+filepath.Join(xdgDir, "wayvncctl-"+strconv.Itoa(i+1)),
				"127.0.0.1", strconv.Itoa(vncPort0+i+1))
			c.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+xdgDir, "WAYLAND_DISPLAY="+socket)
			if err := c.Start(); err == nil {
				procs[i] = c
				alive[i].Store(true)
				go func() { c.Wait(); alive[i].Store(false) }()
			}
		}
		select {
		case <-stop:
			for _, c := range procs {
				if c != nil && c.Process != nil {
					c.Process.Kill()
				}
			}
			return
		case <-time.After(2 * time.Second):
		}
	}
}
