//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"lolssd/hostprofile"
)

const (
	monitorsPath = "/mnt/c/ProgramData/lolssd/monitors.json"
	sessionDir   = "/run/lolssd/session" // в контейнере — inner + "/session"
)

// output — какой вложенный выход Hyprland достался монитору хоста.
type output struct {
	Port, Desc, Name string // eDP-1, «Samsung Display Corp. 0x416E», WAYLAND-1
}

// session запускает рабочий стол хоста: Hyprland вложенным в WSLg, по выходу на каждый
// монитор Windows (docs/ARCHITECTURE.md §6, «Перевод правил, привязанных к мониторам»).
// Конфиг пользователя не меняется: Hyprland получает обёртку, которая переводит имена
// мониторов хоста в имена вложенных выходов.
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
	if b, err := os.ReadFile(monitorsPath); err == nil {
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
	var cfg string
	switch filepath.Ext(p.HyprConfig) {
	case ".lua":
		err = os.WriteFile(filepath.Join(sessionDir, "hyprland.lua"), []byte(luaWrapper(outs, n, p.HyprConfig)), 0o644)
		cfg = inner + "/session/hyprland.lua"
	default:
		cfg, err = translateConf(p.HyprConfig, p.User.Home, outs, n)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "конфиг Hyprland:", err)
		return 1
	}
	return runIn(p, []string{"--bind-ro=" + sessionDir + ":" + inner + "/session"}, []string{"Hyprland", "--config", cfg})
}

// mapOutputs сопоставляет мониторы хоста из профиля с мониторами Windows по EDID.
// Номер выхода — позиция монитора в списке Windows: WAYLAND-1 — первый и т. д.
func mapOutputs(host []hostprofile.Monitor, wins []hostprofile.WinMonitor) []output {
	var res []output
	used := make([]bool, len(wins))
	for _, h := range host {
		for i, w := range wins {
			if !used[i] && hostprofile.SameMonitor(h.EDID, w.EDID) {
				used[i] = true
				res = append(res, output{Port: h.Port, Desc: strings.TrimSpace(h.Desc), Name: fmt.Sprintf("WAYLAND-%d", i+1)})
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
func luaWrapper(outs []output, n int, userConfig string) string {
	var ports, descs strings.Builder
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
local function wrap(t)
  return setmetatable({}, {
    __index = function(_, k)
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
hl = wrap(real)

-- Первый выход Hyprland создаёт сам, остальные — по одному окну на монитор Windows.
real.on("hyprland.start", function()
  for _ = 2, outputs do real.exec_cmd("hyprctl output create wayland") end
end)

package.path = ` + luaStr(filepath.Dir(userConfig)+"/?.lua;") + ` .. package.path
dofile(` + luaStr(userConfig) + `)
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

func translateConf(main, home string, outs []output, n int) (string, error) {
	return translateConfTo(main, home, outs, n, sessionDir)
}

// translateConfTo копирует дерево hyprlang-конфигов (по строкам source = ...) в dir,
// заменяя имена мониторов хоста, и возвращает путь главного конфига внутри контейнера.
// Конфиги читаются из системы хоста, поднятой в rootDir.
func translateConfTo(main, home string, outs []output, n int, dir string) (string, error) {
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
				lines = append(lines, "exec-once = hyprctl output create wayland")
			}
		}
		return inside, os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")), 0o644)
	}
	return copyFile(main)
}
