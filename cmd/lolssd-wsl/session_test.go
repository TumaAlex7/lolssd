//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lolssd/hostprofile"
	"lolssd/keymap"
)

var outs = []output{
	{Port: "eDP-1", Desc: "Samsung Display Corp. 0x416E", Name: "WAYLAND-1"},
	{Port: "DP-9", Desc: "LG Electronics LG HDR WFHD 0x00043804", Name: "WAYLAND-2"},
}

func TestMapOutputs(t *testing.T) {
	edid := func(b byte) []byte { e := make([]byte, 256); e[100] = b; return e }
	host := []hostprofile.Monitor{{Port: "eDP-1", EDID: edid(1)}, {Port: "DP-8", EDID: edid(2)}, {Port: "DP-9", EDID: edid(3)}}
	wins := []hostprofile.WinMonitor{{EDID: edid(3)[:128]}, {EDID: edid(1)}}
	got := mapOutputs(host, wins)
	if len(got) != 2 || got[0].Port != "eDP-1" || got[0].Name != "WAYLAND-2" || got[1].Port != "DP-9" || got[1].Name != "WAYLAND-1" {
		t.Fatal(got)
	}
}

// Обёртка на настоящем Lua с поддельным API Hyprland, который записывает вызовы.
func TestLuaWrapper(t *testing.T) {
	lua, err := exec.LookPath("lua5.4")
	if err != nil {
		t.Skip("нет lua5.4")
	}
	dir := t.TempDir()
	// dsp.exec_cmd — вызываемая таблица, как бывает в API: обёртка должна сохранить вызов.
	os.WriteFile(filepath.Join(dir, "rules.lua"), []byte(`hl.dsp.focusmonitor("DP-9"); hl.dsp.exec_cmd("eDP-1")`), 0o644)
	user := filepath.Join(dir, "hyprland.lua")
	os.WriteFile(user, []byte(`
require("rules")
hl.monitor({ output = "eDP-1", scale = 1.5 })
hl.monitor({ output = "HDMI-A-1" })
hl.monitor({ output = "desc:LG Electronics LG HDR" })
hl.workspace_rule({ workspace = "1", rule = { monitor = "eDP-1" } })
`), 0o644)
	wrapper := filepath.Join(dir, "wrapper.lua")
	os.WriteFile(wrapper, []byte(luaWrapper(outs, 3, user, "", nil)), 0o644)
	fake := filepath.Join(dir, "fake.lua")
	os.WriteFile(fake, []byte(`
local log = {}
local function s(v)
  if type(v) ~= "table" then return tostring(v) end
  local r = {}
  for k, x in pairs(v) do r[#r + 1] = k .. "=" .. s(x) end
  table.sort(r)
  return "{" .. table.concat(r, ",") .. "}"
end
local function rec(name)
  return function(...)
    local p = {}
    for i, v in ipairs({ ... }) do p[i] = s(v) end
    log[#log + 1] = name .. "(" .. table.concat(p, ",") .. ")"
  end
end
local handlers = {}
hl = { monitor = rec("monitor"), workspace_rule = rec("workspace_rule"), exec_cmd = rec("exec_cmd"),
  dsp = { focusmonitor = rec("dsp.focusmonitor"),
    exec_cmd = setmetatable({}, { __call = function(_, ...) rec("dsp.exec_cmd")(...) end }) },
  on = function(_, f) handlers[#handlers + 1] = f end }
dofile(`+luaStr(wrapper)+`)
for _, f in ipairs(handlers) do f() end
print(table.concat(log, "\n"))
`), 0o644)

	out, err := exec.Command(lua, fake).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	want := `dsp.focusmonitor(WAYLAND-2)
dsp.exec_cmd(WAYLAND-1)
monitor({output=WAYLAND-1,scale=1.5})
monitor({output=HDMI-A-1})
monitor({output=WAYLAND-2})
workspace_rule({rule={monitor=WAYLAND-1},workspace=1})
exec_cmd(hyprctl output create wayland)
exec_cmd(hyprctl output create wayland)
`
	if string(out) != want {
		t.Fatalf("получилось:\n%s", out)
	}
}

func TestTranslateConf(t *testing.T) {
	old := rootDir
	rootDir = t.TempDir()
	defer func() { rootDir = old }()
	cfgDir := filepath.Join(rootDir, "home/u/.config/hypr")
	os.MkdirAll(filepath.Join(cfgDir, "conf.d"), 0o755)
	os.WriteFile(filepath.Join(cfgDir, "hyprland.conf"), []byte(
		"monitor = eDP-1, preferred, 0x0, 1.5\n"+
			"monitor = HDMI-A-1, preferred, auto, 1\n"+
			"monitor = desc:LG Electronics LG HDR, preferred, 1920x0, 1\n"+
			"source = ~/.config/hypr/conf.d/*.conf\n"), 0o644)
	os.WriteFile(filepath.Join(cfgDir, "conf.d", "ws.conf"), []byte("workspace = 1, monitor:DP-9\n"), 0o644)

	tmpSession := t.TempDir()
	got, err := translateConfTo("/home/u/.config/hypr/hyprland.conf", "/home/u", outs, 2, "", tmpSession)
	if err != nil {
		t.Fatal(err)
	}
	if got != inner+"/session/conf-0.conf" {
		t.Fatal(got)
	}
	main, _ := os.ReadFile(filepath.Join(tmpSession, "conf-0.conf"))
	ws, _ := os.ReadFile(filepath.Join(tmpSession, "conf-1.conf"))
	want := "monitor = WAYLAND-1, preferred, 0x0, 1.5\n" +
		"monitor = HDMI-A-1, preferred, auto, 1\n" +
		"monitor = WAYLAND-2, preferred, 1920x0, 1\n" +
		"source = " + inner + "/session/conf-1.conf\n" +
		"\nexec-once = hyprctl output create wayland"
	if string(main) != want {
		t.Fatalf("главный конфиг:\n%s", main)
	}
	if !strings.Contains(string(ws), "monitor:WAYLAND-2") {
		t.Fatalf("source-файл:\n%s", ws)
	}
}

// Клавиша Copilot (LMeta+LShift+F23 → правая Meta) на модели bindSuper автора: выбор стороны
// через hl.is_key_down("Super_L"), объект-диспетчер и конфликт с собственной SUPER+SHIFT-привязкой.
func TestLuaChords(t *testing.T) {
	lua, err := exec.LookPath("lua5.4")
	if err != nil {
		t.Skip("нет lua5.4")
	}
	dir := t.TempDir()
	user := filepath.Join(dir, "hyprland.lua")
	os.WriteFile(user, []byte(`
local function isLeft() return hl.is_key_down("Super_L") end
hl.bind("SUPER + 1", function() if isLeft() then print("left 1") else print("right 1") end end)
hl.bind("SUPER + 2", hl.dsp.exec_cmd("two"))
hl.bind("SUPER + 3", function() print("super 3") end)
hl.bind("SUPER + SHIFT + 3", function() print("user shift 3") end)
`), 0o644)
	chords := []keymap.Chord{{Trigger: "F23", Target: "Super_R", TMod: "SUPER", Mods: []string{"SHIFT", "SUPER"}, Hide: []string{"Shift_L", "Super_L"}}}
	wrapper := filepath.Join(dir, "wrapper.lua")
	os.WriteFile(wrapper, []byte(luaWrapper(nil, 1, user, "/kb.xkb", chords)), 0o644)
	fake := filepath.Join(dir, "fake.lua")
	os.WriteFile(fake, []byte(`
local down, binds = {}, {}
local function norm(keys)
  local parts = {}
  for p in keys:gmatch("[^+]+") do parts[#parts + 1] = p:match("^%s*(.-)%s*$"):upper() end
  local key = table.remove(parts)
  table.sort(parts)
  parts[#parts + 1] = key
  return table.concat(parts, "+")
end
hl = {
  is_key_down = function(k) return down[k] == true end,
  bind = function(keys, action) binds[norm(keys)] = action end,
  dsp = { exec_cmd = function(c) return { cmd = c } end },
  config = function(c) print("kb_file " .. c.input.kb_file) end,
  on = function() end, exec_cmd = function() end,
}
dofile(`+luaStr(wrapper)+`)
local function press(keys, mods, key)
  down = {}
  for _, k in ipairs(keys) do down[k] = true end
  local a = binds[norm(mods .. " + " .. key)]
  if type(a) == "function" then a() elseif a then print("dispatch " .. a.cmd) else print("nothing") end
end
press({ "Super_L" }, "SUPER", "1")
press({ "Super_L", "Shift_L", "F23" }, "SUPER + SHIFT", "1")
press({ "Super_L", "Shift_L", "F23" }, "SUPER + SHIFT", "2")
press({ "Super_L", "Shift_L", "F23" }, "SUPER + SHIFT", "3")
press({ "Super_L", "Shift_L" }, "SUPER + SHIFT", "3")
`), 0o644)
	out, err := exec.Command(lua, fake).CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	want := `kb_file /kb.xkb
left 1
right 1
dispatch two
super 3
user shift 3
`
	if string(out) != want {
		t.Fatalf("получилось:\n%s", out)
	}
}
