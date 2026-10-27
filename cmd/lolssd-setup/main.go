//go:build linux

// lolssd-setup — мастер сборки и установки: ISO Windows, куда ставить, что ставить; затем
// make dist, build/iso.sh с файлом опций и sudo build/install.sh. Запускается из репозитория:
// build/lolssd-setup. Не спрашивает то, что спросит сам установщик Windows (язык, редакция, ключ).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"lolssd/tui"
)

const minBytes = 120_000_000_000 // диски «на 128» бывают чуть меньше 128·10⁹ байт

// app — пункт «Удалить лишнее»: один или несколько пакетов (имена — как в tiny11).
type app struct {
	label string
	pkgs  []string
}

var apps = []app{
	{"Xbox Game Bar и приложение Xbox", []string{"Microsoft.GamingApp", "Microsoft.Xbox.TCUI", "Microsoft.XboxGameOverlay", "Microsoft.XboxGamingOverlay", "Microsoft.XboxIdentityProvider", "Microsoft.XboxSpeechToTextOverlay"}},
	{"Copilot", []string{"Microsoft.Copilot"}},
	{"Microsoft 365 (Office), Outlook, To Do", []string{"Microsoft.MicrosoftOfficeHub", "Microsoft.OutlookForWindows", "Microsoft.Todos"}},
	{"Teams", []string{"MSTeams"}},
	{"Связь с телефоном", []string{"Microsoft.YourPhone", "Microsoft.Windows.CrossDevice"}},
	{"Новости, погода, поиск Bing", []string{"Microsoft.BingNews", "Microsoft.BingWeather", "Microsoft.BingSearch"}},
	{"Рекомендации в «Пуске», советы, техподдержка", []string{"Microsoft.StartExperiencesApp", "Microsoft.Getstarted", "Microsoft.GetHelp"}},
	{"Clipchamp (видеоредактор)", []string{"Clipchamp.Clipchamp"}},
	{"Медиаплеер, Кино и ТВ", []string{"Microsoft.ZuneMusic", "Microsoft.ZuneVideo"}},
	{"Paint", []string{"Microsoft.Paint"}},
	{"Камера", []string{"Microsoft.WindowsCamera"}},
	{"Звукозапись", []string{"Microsoft.WindowsSoundRecorder"}},
	{"Часы и будильник", []string{"Microsoft.WindowsAlarms"}},
	{"Карты", []string{"Microsoft.WindowsMaps"}},
	{"Записки", []string{"Microsoft.MicrosoftStickyNotes"}},
	{"Solitaire", []string{"Microsoft.MicrosoftSolitaireCollection"}},
	{"Люди, Семья, Быстрая помощь", []string{"Microsoft.People", "MicrosoftCorporationII.MicrosoftFamily", "MicrosoftCorporationII.QuickAssist"}},
	{"Центр отзывов, Power Automate, Dev Home", []string{"Microsoft.WindowsFeedbackHub", "Microsoft.PowerAutomateDesktop", "Microsoft.Windows.DevHome"}},
}

func main() {
	if !tui.IsTerminal() {
		fmt.Fprintln(os.Stderr, "мастеру нужен терминал")
		os.Exit(2)
	}
	repo, err := os.Getwd()
	if err == nil {
		_, err = os.Stat(filepath.Join(repo, "build", "iso.sh"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "запускайте из корня репозитория: build/lolssd-setup")
		os.Exit(2)
	}
	iso := pickISO()
	if iso == "" {
		return
	}
	t := pickTarget()
	if t == nil {
		return
	}

	updates := &tui.Item{Label: "Windows Update", Hint: "обновления и драйверы через него", On: true}
	vm := &tui.Item{Label: "Linux внутри Windows", Hint: "этот Linux в ВМ, на весь экран", On: true}
	riot := &tui.Item{Label: "Предустановить League of Legends", Hint: "Riot Client и игра — ещё при установке", On: true}
	edge := &tui.Item{Label: "Удалить Edge", Hint: "браузер (его или любой другой) ставится из Microsoft Store", On: true}
	onedrive := &tui.Item{Label: "Удалить OneDrive", On: true}
	extra := &tui.Item{Label: "Удалить лишнее", Hint: "→ — по отдельности"}
	for _, a := range apps {
		extra.Sub = append(extra.Sub, &tui.Item{Label: a.label, On: true})
	}
	packages := &tui.Item{Label: "Компоненты: IE, WordPad, Media Player, рукописный ввод, распознавание речи", On: true}
	access := &tui.Item{Label: "Специальные возможности: диктор, лупа, экранная клавиатура", On: true}
	extra.Sub = append(extra.Sub, packages, access)
	sunshine := &tui.Item{Label: "Sunshine", Hint: "стриминг на другие устройства через Moonlight"}
	user := &tui.Item{Label: "Локальный пользователь lol", Hint: "без пароля, вход сразу; иначе учётку создаёт установщик", On: true}
	items := []*tui.Item{updates, vm, riot, edge, onedrive, extra, sunshine, user}
	header := []string{"Windows: " + filepath.Base(iso), "Куда: " + t.label, "Обои K/DA — всегда."}
	if !tui.Checklist("Что ставить", header, items) {
		return
	}

	fmt.Println("Драйверы именно для этого компьютера (Wi-Fi, звук, чипсет): папка с ними — .inf с файлами")
	fmt.Println("или установщики .exe/.msi. Можно перетащить папку в окно терминала. Пусто — без драйверов.")
	drivers := ""
	for {
		drivers = strings.Trim(tui.Input("Папка с драйверами", ""), `'"`)
		if strings.HasPrefix(drivers, "~/") {
			home, _ := os.UserHomeDir()
			drivers = filepath.Join(home, drivers[2:])
		}
		if st, err := os.Stat(drivers); drivers == "" || err == nil && st.IsDir() {
			break
		}
		fmt.Println("  это не папка")
	}

	placement := "internal"
	if t.usb {
		placement = "external"
	}
	b := func(it *tui.Item) string {
		if it.Checked() {
			return "1"
		}
		return "0"
	}
	var remove []string
	for i, a := range apps {
		if extra.Sub[i].On {
			remove = append(remove, a.pkgs...)
		}
	}
	userName := ""
	if user.On {
		userName = "lol"
	}
	opts := strings.Join([]string{
		"# lolssd: опции сборки, записаны мастером (build/lolssd-setup); описание — build/options.default",
		"placement=" + placement, "vm=" + b(vm), "updates=" + b(updates), "edge=" + b(edge),
		"onedrive=" + b(onedrive), "apps=" + strings.Join(remove, ","), "packages=" + b(packages),
		"accessibility=" + b(access), "riot=" + b(riot), "sunshine=" + b(sunshine), "user=" + userName, "",
	}, "\n")

	var summary []string
	for _, it := range items {
		mark := tui.Green + "✓" + "\x1b[0m"
		if !it.Checked() {
			mark = "·"
		}
		line := mark + " " + it.Label
		if it == extra && it.Checked() {
			n := 0
			for _, s := range it.Sub {
				if s.On {
					n++
				}
			}
			line += fmt.Sprintf(" (%d из %d)", n, len(it.Sub))
		}
		summary = append(summary, line)
	}
	if drivers != "" {
		summary = append(summary, "Драйверы: "+drivers)
	}
	warn := tui.Amber + "Будет создано место под Windows в свободном участке; существующие разделы не меняются." + "\x1b[0m"
	if t.whole {
		warn = tui.Red + "ВСЁ на " + t.dev + " будет стёрто." + "\x1b[0m"
	}
	summary = append([]string{"Windows: " + filepath.Base(iso), "Куда: " + t.label, warn, ""}, summary...)
	if tui.Menu("Проверьте и начните", summary, []string{"Начать: собрать ISO и установить", "Отмена"}) != 0 {
		return
	}

	optsFile := filepath.Join(repo, "dist", "options.txt")
	os.MkdirAll(filepath.Dir(optsFile), 0o755)
	if err := os.WriteFile(optsFile, []byte(opts), 0o644); err != nil {
		fail(err)
	}
	targets := []string{"dist"}
	if vm.On {
		targets = append(targets, "qemu", "tap")
	}
	fmt.Println("== сборка утилит (make " + strings.Join(targets, " ") + ")")
	run("make", targets...)
	fmt.Println("== сборка ISO (около 10 минут на первый раз)")
	out, err := tee(filepath.Join(repo, "build", "iso.sh"), iso, optsFile)
	if err != nil {
		fail(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	built := lines[len(lines)-1]
	args := []string{filepath.Join(repo, "build", "install.sh"), "--free", t.dev, built}
	if t.whole {
		args[1] = "--whole"
	}
	if drivers != "" {
		args = append(args, drivers)
	}
	fmt.Println("== установка (sudo: разметка диска и QEMU)")
	run("sudo", args...)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}

func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("%s: %w", name, err))
	}
}

// tee запускает команду, показывая её вывод, и возвращает его.
func tee(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf strings.Builder
	cmd.Stdout = ioMulti{os.Stdout, &buf}
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return buf.String(), err
}

type ioMulti struct {
	a *os.File
	b *strings.Builder
}

func (m ioMulti) Write(p []byte) (int, error) {
	m.b.Write(p)
	return m.a.Write(p)
}

// pickISO: ISO Windows из ~/Downloads и ~ (кроме собранных lolssd) или путь вручную.
func pickISO() string {
	home, _ := os.UserHomeDir()
	var isos []string
	for _, dir := range []string{filepath.Join(home, "Downloads"), home} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.iso"))
		for _, p := range m {
			if !strings.HasPrefix(filepath.Base(p), "lolssd-") {
				isos = append(isos, p)
			}
		}
	}
	labels := make([]string, len(isos))
	for i, p := range isos {
		st, _ := os.Stat(p)
		labels[i] = fmt.Sprintf("%s  %s(%.1f ГБ)\x1b[0m", filepath.Base(p), "\x1b[2m", float64(st.Size())/1e9)
	}
	labels = append(labels, "Другой файл…")
	i := tui.Menu("ISO Windows 11", []string{"Скачать: microsoft.com/software-download/windows11"}, labels)
	switch {
	case i < 0:
		return ""
	case i < len(isos):
		return isos[i]
	}
	for {
		p := strings.Trim(tui.Input("Путь к ISO", ""), `'"`)
		if p == "" {
			return ""
		}
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
		fmt.Println("  нет такого файла")
	}
}

type target struct {
	dev   string
	whole bool
	usb   bool
	label string
}

type blockdev struct {
	Name        string     `json:"name"`
	Size        int64      `json:"size"`
	Type        string     `json:"type"`
	Tran        string     `json:"tran"`
	Model       string     `json:"model"`
	PTType      string     `json:"pttype"`
	Mountpoints []string   `json:"mountpoints"`
	Children    []blockdev `json:"children"`
}

// pickTarget: весь диск (если он не занят смонтированными разделами) или его самый большой
// свободный участок; только варианты от 120 ГБ.
func pickTarget() *target {
	out, err := exec.Command("lsblk", "--json", "--bytes", "-o", "NAME,SIZE,TYPE,TRAN,MODEL,PTTYPE,MOUNTPOINTS").Output()
	if err != nil {
		fail(err)
	}
	var l struct {
		Blockdevices []blockdev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		fail(err)
	}
	var targets []target
	for _, d := range l.Blockdevices {
		if d.Type != "disk" || strings.HasPrefix(d.Name, "zram") || strings.HasPrefix(d.Name, "loop") {
			continue
		}
		usb := d.Tran == "usb"
		kind := "внутренний"
		if usb {
			kind = "внешний, USB"
		}
		desc := fmt.Sprintf("%s %s (%s, %s)", d.Name, strings.TrimSpace(d.Model), gb(d.Size), kind)
		busy := false
		for _, c := range d.Children {
			for _, m := range c.Mountpoints {
				busy = busy || m != ""
			}
		}
		if free := largestFree(d.Name); d.PTType == "gpt" && len(d.Children) > 0 && free >= minBytes {
			targets = append(targets, target{"/dev/" + d.Name, false, usb, "свободное место " + gb(free) + " на " + desc})
		}
		if !busy && d.Size >= minBytes {
			targets = append(targets, target{"/dev/" + d.Name, true, usb, "весь диск " + desc + " — стереть"})
		}
	}
	if len(targets) == 0 {
		fmt.Println("Нет подходящего места: нужен диск от 120 ГБ целиком (без смонтированных разделов)")
		fmt.Println("или свободный участок от 120 ГБ на диске с разметкой GPT.")
		return nil
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].usb && !targets[j].usb })
	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = t.label
	}
	i := tui.Menu("Куда ставить Windows", []string{
		"Нужно не меньше 120 ГБ. Разделы не ужимаются: только уже свободное место или весь диск.",
	}, labels)
	if i < 0 {
		return nil
	}
	return &targets[i]
}

// largestFree — самый большой свободный участок диска в байтах, по sysfs (без root): промежутки
// между разделами в пределах GPT (первый сектор 2048, последние 33 — резервная копия таблицы).
func largestFree(disk string) int64 {
	sectors := readInt("/sys/block/" + disk + "/size")
	type span struct{ start, end int64 }
	var parts []span
	entries, _ := os.ReadDir("/sys/block/" + disk)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), disk) {
			continue
		}
		s := readInt("/sys/block/" + disk + "/" + e.Name() + "/start")
		n := readInt("/sys/block/" + disk + "/" + e.Name() + "/size")
		parts = append(parts, span{s, s + n})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].start < parts[j].start })
	best, at := int64(0), int64(2048)
	for _, p := range append(parts, span{sectors - 33, sectors - 33}) {
		if p.start-at > best {
			best = p.start - at
		}
		if p.end > at {
			at = p.end
		}
	}
	return best * 512 // в sysfs размеры всегда в 512-байтных секторах
}

func readInt(path string) int64 {
	b, _ := os.ReadFile(path)
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func gb(n int64) string { return fmt.Sprintf("%.0f ГБ", float64(n)/1e9) }
