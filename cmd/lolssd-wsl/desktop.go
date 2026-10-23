//go:build linux

package main

import (
	"crypto/sha256"
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
	// На диске системы хоста: переимпорт пускового дистрибутива (каждое обновление SSD) его не
	// стирает. В /var/tmp — хост сам удаляет оттуда то, к чему не обращались 30 дней
	// (systemd-tmpfiles-clean): если SSD больше не подключать, кэш не останется. По версии
	// aquamarine и хэшу патча.
	aqCache     = "var/tmp/lolssd-aquamarine"
	aqPatch     = "/usr/local/share/lolssd/aquamarine-headless-device.patch"
	xdgDir      = "/run/lolssd/xdg" // XDG_RUNTIME_DIR сеанса, общий с wayvnc
	vncPort0    = 5900              // выход N — порт 5900+N
	aqLibIn     = inner + "/aq/lib" // кэш внутри контейнера
	gbmProbeSrc = "/usr/local/share/lolssd/gbm-probe.c"
)

// runHyprland: vgem, пропатченный aquamarine, Hyprland без экрана и wayvnc на каждый выход.
// Возвращается, когда Hyprland завершился.
func runHyprland(p hostprofile.Profile, wins []hostprofile.WinMonitor, n int, cfg string) int {
	card, render, err := vgemNodes()
	// Узлы создаются root:video/render с правами 0660, а группы пускового Debian — не те, что
	// у пользователя хоста. Отдаём их пользователю сеанса: экрана у vgem нет, это только буферы.
	for _, node := range []string{card, render} {
		if err == nil {
			err = os.Chown(node, p.User.UID, p.User.GID)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	lib, err := ensureAquamarine(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

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
	}

	// Рисует d3d12 через /dev/dxg, а если Hyprland с ним сразу падает — llvmpipe на CPU.
	drivers := []string{"GALLIUM_DRIVER=llvmpipe"}
	if _, err := os.Stat("/dev/dxg"); err == nil {
		// d3d12 (libdxcore) ищет драйвер видеокарты Windows по жёсткому пути /usr/lib/wsl/drivers,
		// поэтому /usr/lib/wsl подключается по тому же пути — только в этом режиме, графика есть
		// только у рабочего стола. Точка подключения — пустой каталог на диске хоста: создаётся
		// один раз и остаётся навсегда (docs/ARCHITECTURE.md §2). Подключение только для чтения,
		// в каталог ничего не пишется, а при аварии оно исчезает вместе с ядром WSL.
		if err := os.MkdirAll(filepath.Join(rootDir, "usr/lib/wsl"), 0o755); err == nil {
			extra = append(extra, "--bind-ro=/usr/lib/wsl:/usr/lib/wsl")
			drivers = append([]string{"GALLIUM_DRIVER=d3d12"}, drivers...)
		} else {
			fmt.Fprintln(os.Stderr, "нет /usr/lib/wsl в системе хоста, рисую на CPU:", err)
		}
	}
	// Управляющие сокеты wayvnc — в пусковом дистрибутиве.
	os.RemoveAll(xdgDir)
	if err := os.MkdirAll(xdgDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	code := 1
	for i, driver := range drivers {
		env := []string{driver, "LP_NUM_THREADS=4",
			"AQ_HEADLESS_DRM_DEVICE=" + card,
			"AQ_HEADLESS_MODES=" + strings.Join(modes, ","),
			"LD_LIBRARY_PATH=" + aqLibIn + ":" + inner + "/lib/lib"}
		// Через start-hyprland, как задумано в Hyprland ≥ 0.53 (иначе он предупреждает в углу
		// экрана): сторожевой процесс, который следит за Hyprland. Построчный вывод (stdbuf):
		// иначе при падении он теряется.
		var command []string
		if _, err := os.Stat(filepath.Join(rootDir, "usr/bin/stdbuf")); err == nil {
			command = append(command, "/usr/bin/stdbuf", "-oL", "-eL")
		}
		if _, err := os.Stat(filepath.Join(rootDir, "usr/bin/start-hyprland")); err == nil {
			command = append(command, "/usr/bin/start-hyprland", "--", "--config", cfg)
		} else {
			command = append(command, "/usr/bin/Hyprland", "--config", cfg)
		}
		name := strings.TrimPrefix(driver, "GALLIUM_DRIVER=")

		fmt.Printf("Hyprland (%s): %s на %s, выходов %d, VNC на 127.0.0.1:%d…%d\n",
			driver, strings.Join(modes, ","), card, n, vncPort0+1, vncPort0+n)
		began := time.Now()
		ns, err := bootDesktop(p, extra, env, command)
		if err != nil {
			fmt.Fprintln(os.Stderr, "запуск системы хоста:", err)
			return 1
		}
		// Сеанс logind: /run/user/UID — в /run контейнера, оттуда его видят wayvnc и копия журнала.
		runtime := ""
		if pid, err := containerInit(ns.Process.Pid); err == nil {
			runtime = fmt.Sprintf("/proc/%d/root/run/user/%d", pid, p.User.UID)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			if runtime != "" {
				serveVNC(n, runtime, stop)
			}
		}()
		// Журнал Hyprland копируется на ходу: при выходе Hyprland удаляет его вместе с каталогом.
		go func() {
			for {
				select {
				case <-stop:
					return
				case <-time.After(2 * time.Second):
					copyHyprLog(runtime, name)
				}
			}
		}()
		code = exitCode(ns.Wait())
		close(stop)
		<-done
		saveCrashReport(p, name, began)
		if code == 0 || time.Since(began) > 30*time.Second {
			break
		}
		gbmProbe(p, extra, append([]string{"env"}, env...), card)
		if i == len(drivers)-1 {
			break
		}
		fmt.Printf("\nHyprland с %s не запустился (код %d), пробую следующий вариант\n\n", driver, code)
	}
	return code
}

const logDir = "/mnt/c/ProgramData/lolssd/" // рядом с session.log, читается и из Linux

// copyHyprLog копирует журнал Hyprland (он в XDG_RUNTIME_DIR сеанса, в памяти) в
// hyprland-<драйвер>.log. Пустой не копируется: последняя удачная копия ценнее.
func copyHyprLog(runtime, driver string) {
	if runtime == "" {
		return
	}
	logs, _ := filepath.Glob(filepath.Join(runtime, "hypr", "*", "hyprland.log"))
	if len(logs) == 0 {
		return
	}
	if b, err := os.ReadFile(logs[len(logs)-1]); err == nil && len(b) > 0 {
		os.WriteFile(logDir+"hyprland-"+driver+".log", b, 0o644)
	}
}

// saveCrashReport переносит краш-репорт попытки в crash-<драйвер>.txt. Hyprland пишет его в
// ~/.cache/hyprland хоста под номером процесса — в контейнере он всегда один и тот же, и
// следующий отчёт перезаписывал предыдущий не целиком. Отчёт сеанса с хоста убирается.
func saveCrashReport(p hostprofile.Profile, driver string, since time.Time) {
	reports, _ := filepath.Glob(filepath.Join(rootDir, p.User.Home, ".cache/hyprland/hyprlandCrashReport*.txt"))
	for _, r := range reports {
		st, err := os.Stat(r)
		if err != nil || st.ModTime().Before(since) {
			continue
		}
		if b, err := os.ReadFile(r); err == nil && os.WriteFile(logDir+"crash-"+driver+".txt", b, 0o644) == nil {
			os.Remove(r)
		}
	}
}

// gbmProbe собирает в контейнере build/hypr/gbm-probe.c и запускает его с окружением Hyprland:
// почему GBM не создаётся на устройстве (dlopen библиотек Mesa, драйвер, gbm_create_device).
// Вывод — в консоль и session.log.
func gbmProbe(p hostprofile.Profile, extra, env []string, card string) {
	src, err := os.ReadFile(gbmProbeSrc)
	if err == nil {
		err = os.WriteFile(filepath.Join(sessionDir, "gbm-probe.c"), src, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gbm-probe:", err)
		return
	}
	fmt.Println("\nдиагностика GBM:")
	script := `set -e; out=$(mktemp); cc -o "$out" ` + inner + `/session/gbm-probe.c -ldl
ls -l /dev/dri; ls "$1" 2>/dev/null | head -20; "$out" ` + card
	args := append(append([]string{}, env...), "sh", "-c", script, "sh", inner+"/lib/lib")
	runIn(p, extra, args)
	fmt.Println()
}

// vgemNodes загружает vgem и возвращает его узлы: основной и render. Буферы aquamarine выделяет
// через основной (через render vgem выделять не даёт), а рендерер Hyprland, когда EGL не может
// открыть устройство напрямую, создаёт GBM для EGL на render-узле.
func vgemNodes() (card, render string, err error) {
	if out, err := exec.Command("modprobe", "vgem").CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("modprobe vgem: %v: %s", err, out)
	}
	node := regexp.MustCompile(`/(card|renderD)\d+$`)
	for range 20 {
		nodes, _ := filepath.Glob("/sys/class/drm/*")
		for _, c := range nodes {
			m := node.FindStringSubmatch(c)
			if m == nil {
				continue
			}
			if dev, _ := filepath.EvalSymlinks(filepath.Join(c, "device")); filepath.Base(dev) != "vgem" {
				continue
			}
			if m[1] == "card" {
				card = "/dev/dri/" + filepath.Base(c)
			} else {
				render = "/dev/dri/" + filepath.Base(c)
			}
		}
		if card != "" && render != "" {
			return card, render, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", "", fmt.Errorf("после modprobe vgem его узлы не появились в /sys/class/drm (card %q, render %q)", card, render)
}

// ensureAquamarine возвращает пропатченный libaquamarine под версию хоста. Собирается один раз
// на версию — внутри системы хоста, её же компилятором и заголовками (ABI совпадает с её
// Hyprland), — и хранится в пусковом дистрибутиве, на диск хоста ничего не пишется.
func ensureAquamarine(p hostprofile.Profile) (string, error) {
	libs, _ := filepath.Glob(filepath.Join(rootDir, "usr/lib/libaquamarine.so.*.*.*"))
	if len(libs) == 0 {
		return "", fmt.Errorf("в системе хоста нет libaquamarine (Hyprland не установлен?)")
	}
	base := filepath.Base(libs[0])                           // libaquamarine.so.0.15.1
	version := strings.TrimPrefix(base, "libaquamarine.so.") // 0.15.1, тег v0.15.1
	patch, err := os.ReadFile(aqPatch)
	if err != nil {
		return "", fmt.Errorf("патч aquamarine: %w", err)
	}
	dir := filepath.Join(rootDir, aqCache, fmt.Sprintf("%s-%x", version, sha256.Sum256(patch))[:len(version)+9])
	lib := filepath.Join(dir, "lib", base)
	if _, err := os.Stat(lib); err == nil {
		pruneAquamarine(dir)
		return lib, nil
	}

	fmt.Printf("Собираю aquamarine %s с патчем под систему хоста (один раз на версию и патч)…\n", version)
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.Chown(dir, p.User.UID, p.User.GID)
	}
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
	pruneAquamarine(dir)
	return lib, nil
}

// pruneAquamarine удаляет из кэша сборки других версий aquamarine и патча: нужна только текущая.
func pruneAquamarine(keep string) {
	old, _ := filepath.Glob(filepath.Join(filepath.Dir(keep), "*"))
	for _, d := range old {
		if d != keep {
			os.RemoveAll(d)
		}
	}
}

// serveVNC ждёт сокет Hyprland и выходы HEADLESS-1…n и держит на каждом по wayvnc,
// пока не закроют stop. wayvnc работает в пусковом дистрибутиве и подключается к сокету
// Hyprland через общий XDG_RUNTIME_DIR.
func serveVNC(n int, runtime string, stop chan struct{}) {
	var socket string
	for socket == "" {
		select {
		case <-stop:
			return
		case <-time.After(300 * time.Millisecond):
		}
		matches, _ := filepath.Glob(filepath.Join(runtime, "wayland-*"))
		for _, m := range matches {
			if !strings.HasSuffix(m, ".lock") {
				socket = m // полный путь: libwayland принимает его в WAYLAND_DISPLAY
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
			// Курсор Hyprland рисует в самой картинке (--render-cursor): отдельный захват курсора
			// в wayvnc 0.9 при сбое завершает его целиком, его отключает wayvnc-wlr-only.so
			// (build/hypr/wayvnc-wlr-only.c).
			c := exec.Command("wayvnc", "--output=HEADLESS-"+strconv.Itoa(i+1), "--max-fps=60", "--render-cursor",
				"--socket="+filepath.Join(xdgDir, "wayvncctl-"+strconv.Itoa(i+1)),
				"127.0.0.1", strconv.Itoa(vncPort0+i+1))
			c.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+xdgDir, "WAYLAND_DISPLAY="+socket,
				"LD_PRELOAD=/usr/local/lib/lolssd/wayvnc-wlr-only.so")
			f, _ := os.OpenFile(fmt.Sprintf("%swayvnc-%d.log", logDir, i+1), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if f != nil {
				c.Stdout, c.Stderr = f, f
			}
			if err := c.Start(); err == nil {
				procs[i] = c
				alive[i].Store(true)
				go func() {
					c.Wait()
					alive[i].Store(false)
					if f != nil {
						f.Close()
					}
				}()
			} else if f != nil {
				f.Close()
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
