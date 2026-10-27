# Сборка lolssd. Раскладка dist/ повторяет то, куда файлы ложатся на SSD:
#   dist/LOLSSD/          содержимое раздела LOLSSD (exFAT): утилита для Linux, профили, драйверы
#   dist/windows/C/       файлы, которые кладутся в раздел Windows
#   dist/windows/lolssd*.reg  настройки реестра; build/iso.sh склеивает нужные по опциям
#   dist/qemu/, dist/tap/ QEMU и драйвер TAP для ВМ с системой хоста (make qemu, make tap)

GO      ?= go
GOFLAGS := -trimpath -ldflags=-s\ -w
export CGO_ENABLED := 0

WIN := dist/windows/C
.PHONY: dist qemu tap iso test clean

dist:
	mkdir -p dist/LOLSSD/profiles dist/LOLSSD/drivers "$(WIN)/Program Files/lolssd" "$(WIN)/Windows/Setup/Scripts"
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o dist/LOLSSD/lolssd ./cmd/lolssd
	# windowsgui: задания планировщика не открывают окно консоли; журнал пишется в файл.
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w -H windowsgui" -o "$(WIN)/Program Files/lolssd/lolssd-win.exe" ./cmd/lolssd-win
	# Файлы для Windows — с CRLF: cmd и reg import на LF спотыкаются.
	for f in SetupComplete.cmd lolssd-setup.ps1; do sed 's/\r*$$/\r/' build/setup/$$f > "$(WIN)/Windows/Setup/Scripts/$$f"; done
	for f in build/setup/lolssd*.reg; do sed 's/\r*$$/\r/' $$f > dist/windows/$$(basename $$f); done
	for f in lolssd-drivers.ps1 lolssd-gpu.ps1 lolssd-riot.ps1; do sed 's/\r*$$/\r/' build/setup/$$f > "$(WIN)/Program Files/lolssd/$$f"; done
	cp README.md dist/LOLSSD/README.md

# QEMU для Windows (ВМ с системой хоста, lolssd-win desktop): dist/qemu, build/qemu.sh.
qemu:
	build/qemu.sh

# Драйвер TAP-Windows для сети ВМ: dist/tap, build/tap.sh.
tap:
	build/tap.sh

# Установочный ISO со всем внутри: make iso ISO=~/Downloads/Win11.iso [OPTIONS=файл опций]
# (по умолчанию build/options.default; обычно ISO собирает мастер build/lolssd-setup).
OPTIONS ?= build/options.default
iso: dist $(if $(filter vm=1,$(shell grep -x 'vm=1' $(OPTIONS))),qemu tap)
	@test -n "$(ISO)" || { echo "укажите ISO=путь/к/Windows.iso"; exit 1; }
	build/iso.sh "$(ISO)" "$(OPTIONS)"

test:
	$(GO) vet ./...
	GOOS=windows $(GO) vet ./cmd/lolssd-win
	$(GO) test ./...

clean:
	rm -rf dist
