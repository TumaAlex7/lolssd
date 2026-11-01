//go:build linux

// lolssd-setup — мастер установки: стоковый ISO Windows, куда ставить, что ставить; затем
// sudo install.sh из вшитого набора файлов (payload, make setup). Один бинарник: ни Go, ни
// репозиторий не нужны, из пакетов — QEMU, OVMF и swtpm. Не спрашивает то, что спросит сам
// установщик Windows (язык, редакция, ключ).
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"lolssd/tui"
)

//go:embed all:payload
var payload embed.FS

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
	{"Связь с телефоном", []string{"Microsoft.YourPhone", "MicrosoftWindows.CrossDevice"}},
	{"Виджеты, новости, погода, поиск Bing", []string{"MicrosoftWindows.Client.WebExperience", "Microsoft.BingNews", "Microsoft.BingWeather", "Microsoft.BingSearch"}},
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
	if _, err := payload.ReadFile("payload/install.sh"); err != nil {
		fmt.Fprintln(os.Stderr, "в мастере нет набора файлов: собирайте его make setup")
		os.Exit(2)
	}
	if !checkDeps() {
		os.Exit(1)
	}
	defer removeDeps()
	iso := pickISO()
	if iso == "" {
		return
	}
	t := pickTarget()
	if t == nil {
		return
	}

	updates := &tui.Item{Label: "Windows Update", Hint: "с ним приходят и программы производителя (службы ASUS и т. п.); видеодрайвер и без него поставит установщик AMD/NVIDIA/Intel из образа"}
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
	warn := tui.Amber + "В начале свободного участка будет создан раздел LOLSSD; существующие разделы не меняются." + "\x1b[0m"
	if t.whole {
		warn = tui.Red + "ВСЁ на " + t.dev + " будет стёрто." + "\x1b[0m"
	}
	summary = append([]string{"Windows: " + filepath.Base(iso), "Куда: " + t.label, warn, ""}, summary...)
	if tui.Menu("Проверьте и начните", summary, []string{"Начать установку", "Отмена"}) != 0 {
		return
	}

	dir, err := unpack()
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "options.txt"), []byte(opts), 0o644); err != nil {
		fail(err)
	}
	mode := "--free"
	if t.whole {
		mode = "--whole"
	}
	// sudo очищает окружение, оставляя DISPLAY: без WAYLAND_DISPLAY и XDG_RUNTIME_DIR окно QEMU
	// открылось бы через XWayland (мыльное при дробном масштабе, другой класс окна). Передаём их
	// в самой команде: env после sudo работает при любом env_reset.
	args := []string{"env"}
	for _, v := range []string{"WAYLAND_DISPLAY", "DISPLAY", "XDG_RUNTIME_DIR"} {
		if s := os.Getenv(v); s != "" {
			args = append(args, v+"="+s)
		}
	}
	args = append(args, "bash", filepath.Join(dir, "install.sh"), mode, t.dev, iso, dir)
	if drivers != "" {
		args = append(args, drivers)
	}
	fmt.Println("== установка (sudo: разметка диска и доступ QEMU к нему)")
	run("sudo", args...)
	os.RemoveAll(dir)
}

// unpack раскладывает вшитый набор файлов во временный каталог.
func unpack() (string, error) {
	dir, err := os.MkdirTemp("", "lolssd-setup-")
	if err != nil {
		return "", err
	}
	sub, _ := fs.Sub(payload, "payload")
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		to := filepath.Join(dir, p)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if p == "install.sh" || p == "LOLSSD/lolssd" {
			mode = 0o755
		}
		return os.WriteFile(to, b, mode)
	})
	return dir, err
}

// Пакеты для установки: QEMU (с окном GTK и ПЗУ сетевой карты), прошивка OVMF с Secure Boot и
// swtpm (виртуальный TPM). Как сборочные зависимости у yay: мастер ставит недостающее сам и после
// установки удаляет ровно то, что поставил (разница списков пакетов до и после).
type pkgManager struct {
	pkgs    []string
	list    []string // все установленные пакеты, по имени в строке
	install []string
	remove  []string
}

var managers = map[string]pkgManager{
	"pacman": {[]string{"qemu-system-x86", "qemu-system-x86-firmware", "qemu-ui-gtk", "edk2-ovmf", "swtpm"},
		[]string{"pacman", "-Qq"}, []string{"pacman", "-S", "--needed", "--asdeps", "--noconfirm"}, []string{"pacman", "-Rn", "--noconfirm"}},
	"apt-get": {[]string{"qemu-system-x86", "qemu-system-gui", "ovmf", "swtpm-tools"},
		[]string{"dpkg-query", "-W", "-f", "${Package}\\n"}, []string{"apt-get", "install", "-y", "--no-install-recommends"}, []string{"apt-get", "purge", "-y"}},
	"dnf": {[]string{"qemu-system-x86-core", "qemu-ui-gtk", "edk2-ovmf", "swtpm-tools"},
		[]string{"rpm", "-qa", "--qf", "%{NAME}\\n"}, []string{"dnf", "install", "-y"}, []string{"dnf", "remove", "-y"}},
	"zypper": {[]string{"qemu-x86", "qemu-ui-gtk", "qemu-ovmf-x86_64", "swtpm"},
		[]string{"rpm", "-qa", "--qf", "%{NAME}\\n"}, []string{"zypper", "--non-interactive", "install"}, []string{"zypper", "--non-interactive", "remove"}},
}

var (
	pm        pkgManager
	tempPkgs  []string // поставлены мастером, удаляются в конце
	ovmfFiles = []string{"/usr/share/edk2/x64/OVMF_CODE.secboot.4m.fd", "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
		"/usr/share/edk2/ovmf/OVMF_CODE.secboot.fd", "/usr/share/OVMF/OVMF_CODE.secboot.fd"}
)

func missingDeps() []string {
	var missing []string
	for _, t := range []string{"qemu-system-x86_64", "swtpm"} {
		if _, err := exec.LookPath(t); err != nil {
			missing = append(missing, t)
		}
	}
	for _, f := range ovmfFiles {
		if _, err := os.Stat(f); err == nil {
			return missing
		}
	}
	return append(missing, "OVMF")
}

func installed() map[string]bool {
	out, _ := exec.Command(pm.list[0], pm.list[1:]...).Output()
	m := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		m[l] = true
	}
	return m
}

func checkDeps() bool {
	missing := missingDeps()
	if len(missing) == 0 {
		return true
	}
	for _, name := range []string{"pacman", "apt-get", "dnf", "zypper"} {
		if _, err := exec.LookPath(name); err == nil {
			pm = managers[name]
			break
		}
	}
	fmt.Println("Для установки Windows в виртуальной машине не хватает:", strings.Join(missing, ", "))
	if pm.pkgs == nil {
		fmt.Println("Поставьте пакеты QEMU (x86_64), OVMF (edk2) и swtpm и запустите мастер снова.")
		return false
	}
	fmt.Println("Мастер может поставить их на время установки и удалить после, как yay сборочные")
	fmt.Println("зависимости: " + strings.Join(pm.pkgs, " ") + " (что уже стоит, не тронет).")
	if a := strings.ToLower(tui.Input("Поставить", "да")); a != "да" && a != "д" && a != "y" && a != "yes" {
		return false
	}
	before := installed()
	cmd := exec.Command("sudo", append(pm.install, pm.pkgs...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	for p := range installed() {
		if !before[p] {
			tempPkgs = append(tempPkgs, p)
		}
	}
	if err != nil {
		fmt.Println("не получилось поставить:", err)
		removeDeps()
		return false
	}
	if m := missingDeps(); len(m) > 0 {
		fmt.Println("после установки пакетов всё ещё нет:", strings.Join(m, ", "))
		removeDeps()
		return false
	}
	return true
}

// removeDeps удаляет пакеты, которые поставил мастер, — и только их.
func removeDeps() {
	if len(tempPkgs) == 0 {
		return
	}
	sort.Strings(tempPkgs)
	fmt.Printf("== удаляю поставленное для установки (%d пакетов): %s\n", len(tempPkgs), strings.Join(tempPkgs, " "))
	cmd := exec.Command("sudo", append(pm.remove, tempPkgs...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Println("не получилось удалить, удалите вручную:", strings.Join(tempPkgs, " "))
	}
	tempPkgs = nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	removeDeps()
	os.Exit(1)
}

func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail(fmt.Errorf("%s: %w", name, err))
	}
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

// pickTarget: пользователь выбирает диск, способ решает мастер. Есть свободный участок от 120 ГБ —
// Windows встаёт туда, остальное на диске не трогается; нет — диск стирается целиком, если он не
// занят смонтированными разделами. Диски, куда не встать никак, — в шапке с причиной.
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
	var unfit []string
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
		free := largestFree(d.Name)
		switch {
		case d.PTType == "gpt" && len(d.Children) > 0 && free >= minBytes+512<<20: // + LOLSSD
			targets = append(targets, target{"/dev/" + d.Name, false, usb, desc + " — в свободные " + gb(free)})
		case d.Size < minBytes:
			unfit = append(unfit, "  "+desc+": меньше 120 ГБ")
		case !busy:
			targets = append(targets, target{"/dev/" + d.Name, true, usb, desc + " — стереть весь диск"})
		default:
			unfit = append(unfit, "  "+desc+": на нём работает система, а свободного места меньше 120 ГБ")
		}
	}
	if len(targets) == 0 {
		fmt.Println("Нет подходящего диска: нужен свободный участок от 120 ГБ или незанятый диск от 120 ГБ.")
		for _, u := range unfit {
			fmt.Println(u)
		}
		return nil
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].usb && !targets[j].usb })
	labels := make([]string, len(targets))
	for i, t := range targets {
		labels[i] = t.label
	}
	header := []string{"Есть свободное место от 120 ГБ — Windows встанет туда, остальное не тронется;",
		"нет — диск будет стёрт целиком. Разделы не ужимаются. Сам мастер создаёт только раздел",
		"LOLSSD (512 МБ); остальное размечает установщик Windows: выберите там «Незанятое место»."}
	if len(unfit) > 0 {
		header = append(header, "", "Не подходят:")
		header = append(header, unfit...)
	}
	i := tui.Menu("На какой диск ставить Windows", header, labels)
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
