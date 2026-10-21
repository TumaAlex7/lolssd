//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"lolssd/hostprofile"
	"lolssd/keymap"
)

const (
	monitorsPath = "/mnt/c/ProgramData/lolssd/monitors.json"
	sessionDir   = "/run/lolssd/session" // в контейнере — inner + "/session"
)

// output — какой вложенный выход Hyprland достался монитору хоста.
type output struct {
	Port, Desc, Name string // eDP-1, «Samsung Display Corp. 0x416E», HEADLESS-1
}

// session запускает рабочий стол хоста: Hyprland без экрана (headless) на виртуальном DRM-устройстве
// vgem, с одним выходом на каждый монитор Windows; картинку каждого выхода отдаёт wayvnc, а Windows
// показывает её полноэкранным TigerVNC (docs/ARCHITECTURE.md §6). Конфиг пользователя не меняется:
// Hyprland получает обёртку, которая переводит имена мониторов хоста в имена выходов.
func session() int {
	if code := up(); code != 0 {
		return code
	}
	p, err := loadProfile()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if p.HyprConfig == "" {
		fmt.Fprintln(os.Stderr, "режим «Рабочий стол» пока умеет только Hyprland; программы можно запускать через lolssd-wsl run")
		return 1
	}

	var wins []hostprofile.WinMonitor
	// Текущие мониторы передаёт lolssd-win desktop (WSLENV); monitors.json — снимок при входе.
	if env := os.Getenv("LOLSSD_MONITORS"); env != "" {
		json.Unmarshal([]byte(env), &wins)
	} else if b, err := os.ReadFile(monitorsPath); err == nil {
		json.Unmarshal(b, &wins)
	}
	n := max(len(wins), 1)
	outs := mapOutputs(p.Monitors, wins)
	for _, o := range outs {
		fmt.Printf("  %-10s → %s (%s)\n", o.Port, o.Name, o.Desc)
	}
	if len(outs) < len(wins) {
		fmt.Printf("  мониторов без правил хоста: %d (их не было подключено при lolssd profile)\n", len(wins)-len(outs))
	}

	os.RemoveAll(sessionDir)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	plan := keymap.Build(keymap.ParseKeyd(p.Keyboard.Keyd))
	kbFile := ""
	if err := writeKeymap(p.Keyboard, plan); err != nil {
		fmt.Fprintln(os.Stderr, "внимание: раскладка для Super не собрана, Super в Hyprland работать не будет:", err)
	} else {
		kbFile = inner + "/session/keymap.xkb"
	}

	var cfg string
	switch filepath.Ext(p.HyprConfig) {
	case ".lua":
		err = os.WriteFile(filepath.Join(sessionDir, "hyprland.lua"), []byte(luaWrapper(outs, n, p.HyprConfig, kbFile, plan.Chords)), 0o644)
		cfg = inner + "/session/hyprland.lua"
	default:
		if len(plan.Chords) > 0 {
			fmt.Fprintln(os.Stderr, "внимание: сочетания keyd (например, клавиша Copilot как Meta) поддерживаются только для Lua-конфига Hyprland")
		}
		cfg, err = translateConf(p.HyprConfig, p.User.Home, outs, n, kbFile)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "конфиг Hyprland:", err)
		return 1
	}
	return runHyprland(p, wins, n, cfg)
}

// writeKeymap собирает раскладку Hyprland: RMLVO пользователя (xkbcli из пускового дистрибутива)
// плюс клавиши плана: F13/F14 → Super_L/Super_R и «главные» клавиши сочетаний.
func writeKeymap(kb hostprofile.Keyboard, plan keymap.Plan) error {
	args := []string{"compile-keymap"}
	for flag, v := range map[string]string{"--layout": kb.Layout, "--variant": kb.Variant, "--options": kb.Options, "--model": kb.Model} {
		if v = strings.ReplaceAll(v, " ", ""); v != "" {
			args = append(args, flag, v)
		}
	}
	out, err := exec.Command("xkbcli", args...).Output()
	if err != nil {
		return fmt.Errorf("xkbcli %v: %w", args, err)
	}
	patched, err := keymap.PatchXKB(string(out), plan)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(sessionDir, "keymap.xkb"), []byte(patched), 0o644)
}

// mapOutputs сопоставляет мониторы хоста из профиля с мониторами Windows по EDID.
// Номер выхода — позиция монитора в списке Windows: HEADLESS-1 — первый и т. д.
func mapOutputs(host []hostprofile.Monitor, wins []hostprofile.WinMonitor) []output {
	var res []output
	used := make([]bool, len(wins))
	for _, h := range host {
		for i, w := range wins {
			if !used[i] && hostprofile.SameMonitor(h.EDID, w.EDID) {
				used[i] = true
				res = append(res, output{Port: h.Port, Desc: strings.TrimSpace(h.Desc), Name: fmt.Sprintf("HEADLESS-%d", i+1)})
				break
			}
		}
	}
	return res
}

// luaWrapper: обёртка для Lua-конфига. Подменяет глобальную таблицу hl прокси, которое
// в аргументах любого вызова (на любой глубине таблиц) заменяет имена мониторов хоста:
// порт целиком и «desc:» по префиксу, как сопоставляет сам Hyprland. Так перевод не зависит
// от того, какие функции API принимают имя монитора. Затем подключает конфиг пользователя.
func luaWrapper(outs []output, n int, userConfig, kbFile string, chords []keymap.Chord) string {
	var ports, descs, chordsLua strings.Builder
	for _, c := range chords {
		var mods, hide []string
		for _, m := range c.Mods {
			mods = append(mods, luaStr(m))
		}
		for _, h := range c.Hide {
			hide = append(hide, luaStr(h))
		}
		fmt.Fprintf(&chordsLua, "  { trigger = %s, target = %s, tmod = %s, mods = { %s }, hide = { %s } },\n",
			luaStr(c.Trigger), luaStr(c.Target), luaStr(c.TMod), strings.Join(mods, ", "), strings.Join(hide, ", "))
	}
	kb := "nil"
	if kbFile != "" {
		kb = luaStr(kbFile)
	}
	for _, o := range outs {
		fmt.Fprintf(&ports, "  [ %s ] = %s,\n", luaStr(o.Port), luaStr(o.Name))
		if o.Desc != "" {
			fmt.Fprintf(&descs, "  { %s, %s },\n", luaStr(o.Desc), luaStr(o.Name))
		}
	}
	return `-- lolssd: обёртка конфига Hyprland для режима «Linux внутри Windows», создана lolssd-wsl.
local ports = {
` + ports.String() + `}
local descs = {
` + descs.String() + `}
local outputs = ` + fmt.Sprint(n) + `
local kbFile = ` + kb + `
-- Сочетания keyd, дающие модификатор (клавиша Copilot = LMeta+LShift+F23 → правая Meta):
-- пока зажат trigger, считается зажатым target, а модификаторы сочетания «не видны».
local chords = {
` + chordsLua.String() + `}

local function tr(v)
  if type(v) == "string" then
    if ports[v] then return ports[v] end
    if v:sub(1, 5) == "desc:" then
      local want = v:sub(6)
      for _, d in ipairs(descs) do
        if d[1]:sub(1, #want) == want then return d[2] end
      end
    end
    return v
  end
  if type(v) == "table" and getmetatable(v) == nil then
    local r = {}
    for k, x in pairs(v) do r[k] = tr(x) end
    return r
  end
  return v
end

local function call(f, ...)
  local a = table.pack(...)
  for i = 1, a.n do a[i] = tr(a[i]) end
  return f(table.unpack(a, 1, a.n))
end

-- Таблицы тоже оборачиваются, с сохранением вызова (у API бывают вызываемые таблицы).
-- overrides — подменённые функции верхнего уровня hl.
local function wrap(t, overrides)
  return setmetatable({}, {
    __index = function(_, k)
      if overrides and overrides[k] then return overrides[k] end
      local x = t[k]
      if type(x) == "function" then
        return function(...) return call(x, ...) end
      elseif type(x) == "table" then
        return wrap(x)
      end
      return x
    end,
    __newindex = t,
    __call = function(_, ...) return call(t, ...) end,
  })
end

local real = hl
local binds = {}
local overrides = {}

overrides.is_key_down = function(name)
  for _, c in ipairs(chords) do
    if real.is_key_down(c.trigger) then
      if name == c.target then return true end
      for _, h in ipairs(c.hide) do
        if name == h then return false end
      end
    end
  end
  return call(real.is_key_down, name)
end

-- Привязки откладываются до конца конфига: для сочетаний нужны все сразу.
overrides.bind = function(keys, action, opts)
  binds[#binds + 1] = { keys = keys, action = action, opts = opts }
end

hl = wrap(real, overrides)

-- Первый выход Hyprland создаёт сам, остальные — по одному на монитор Windows (размеры из AQ_HEADLESS_MODES).
real.on("hyprland.start", function()
  for _ = 2, outputs do real.exec_cmd("hyprctl output create headless") end
end)

package.path = ` + luaStr(filepath.Dir(userConfig)+"/?.lua;") + ` .. package.path
dofile(` + luaStr(userConfig) + `)

-- Привязки. Зажатое сочетание приходит с лишними модификаторами (у Copilot это Shift), поэтому
-- к привязке «SUPER + K» добавляется «SHIFT + SUPER + K», которая срабатывает при зажатом trigger.
-- Если у пользователя есть своя такая привязка и обе — функции, они разводятся по trigger;
-- иначе пользовательская важнее.
local function parse(keys)
  local parts = {}
  for p in keys:gmatch("[^+]+") do parts[#parts + 1] = p:match("^%s*(.-)%s*$") end
  local key = table.remove(parts)
  local mods = {}
  for _, m in ipairs(parts) do mods[m:upper()] = true end
  return mods, key
end
local function join(mods, key)
  local list = {}
  for m in pairs(mods) do list[#list + 1] = m end
  table.sort(list)
  list[#list + 1] = key
  return table.concat(list, " + ")
end

local byKeys, variants = {}, {}
for _, b in ipairs(binds) do
  local m, k = parse(b.keys)
  b.norm = join(m, k)
  byKeys[b.norm] = b
end
for _, b in ipairs(binds) do
  local mods, key = parse(b.keys)
  for _, c in ipairs(chords) do
    if mods[c.tmod] then
      local vm = {}
      for m in pairs(mods) do
        if m ~= c.tmod then vm[m] = true end
      end
      for _, m in ipairs(c.mods) do vm[m] = true end
      local v = join(vm, key)
      if v ~= b.norm and not variants[v] then
        variants[v] = { chord = c, orig = b, user = byKeys[v] }
      end
    end
  end
end
for _, b in ipairs(binds) do
  if not variants[b.norm] then call(real.bind, b.keys, b.action, b.opts) end
end
for v, x in pairs(variants) do
  if not x.user then
    call(real.bind, v, x.orig.action, x.orig.opts)
  elseif type(x.orig.action) == "function" and type(x.user.action) == "function" then
    call(real.bind, v, function(...)
      if real.is_key_down(x.chord.trigger) then return x.orig.action(...) end
      return x.user.action(...)
    end, x.user.opts)
  else
    call(real.bind, x.user.keys, x.user.action, x.user.opts)
  end
end

-- Раскладка с F13/F14 → Super_L/Super_R (Windows отдаёт Meta так) и клавишами сочетаний.
if kbFile then real.config({ input = { kb_file = kbFile } }) end

-- Журнал ещё и в stdout: lolssd-win пишет его в session.log, а файл журнала Hyprland при
-- аварийном завершении не успевает записаться.
real.config({ debug = { enable_stdout_logs = true } })
`
}

// luaStr — длинная строка Lua: в ней ничего не экранируется.
func luaStr(s string) string {
	eq := "="
	for strings.Contains(s, "]"+eq+"]") {
		eq += "="
	}
	return "[" + eq + "[" + s + "]" + eq + "]"
}

func translateConf(main, home string, outs []output, n int, kbFile string) (string, error) {
	return translateConfTo(main, home, outs, n, kbFile, sessionDir)
}

// translateConfTo копирует дерево hyprlang-конфигов (по строкам source = ...) в dir,
// заменяя имена мониторов хоста, и возвращает путь главного конфига внутри контейнера.
// Конфиги читаются из системы хоста, поднятой в rootDir.
func translateConfTo(main, home string, outs []output, n int, kbFile, dir string) (string, error) {
	var res []*regexp.Regexp
	var names []string
	for _, o := range outs {
		res = append(res, regexp.MustCompile(`(^|[\s,=:])`+regexp.QuoteMeta(o.Port)+`($|[\s,])`))
		names = append(names, o.Name)
	}
	descRe := regexp.MustCompile(`desc:[^,#\n]*`)
	sourceRe := regexp.MustCompile(`^(\s*source\s*=\s*)(\S+)\s*$`)

	copies := map[string]string{} // путь хоста → путь копии внутри контейнера
	var copyFile func(path string) (string, error)
	copyFile = func(path string) (string, error) {
		if c, ok := copies[path]; ok {
			return c, nil
		}
		name := fmt.Sprintf("conf-%d.conf", len(copies))
		inside := inner + "/session/" + name
		copies[path] = inside
		b, err := os.ReadFile(filepath.Join(rootDir, path))
		if err != nil {
			return "", err
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if m := sourceRe.FindStringSubmatch(line); m != nil {
				pattern := strings.Replace(m[2], "~", home, 1)
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(filepath.Dir(path), pattern)
				}
				found, _ := filepath.Glob(filepath.Join(rootDir, pattern))
				var repl []string
				for _, f := range found {
					c, err := copyFile(strings.TrimPrefix(f, rootDir))
					if err != nil {
						return "", err
					}
					repl = append(repl, m[1]+c)
				}
				lines[i] = strings.Join(repl, "\n")
				continue
			}
			line = descRe.ReplaceAllStringFunc(line, func(d string) string {
				want := strings.TrimSpace(strings.TrimPrefix(d, "desc:"))
				for _, o := range outs {
					if o.Desc != "" && strings.HasPrefix(o.Desc, want) {
						return o.Name
					}
				}
				return d
			})
			for j, re := range res {
				line = re.ReplaceAllString(line, "${1}"+names[j]+"${2}")
			}
			lines[i] = line
		}
		if inside == inner+"/session/conf-0.conf" {
			for range n - 1 {
				lines = append(lines, "exec-once = hyprctl output create headless")
			}
			if kbFile != "" {
				lines = append(lines, "input {", "    kb_file = "+kbFile, "}")
			}
		}
		return inside, os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")), 0o644)
	}
	return copyFile(main)
}
