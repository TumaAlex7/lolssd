# Сборка lolssd. Раскладка dist/ повторяет то, куда файлы ложатся на SSD:
#   dist/LOLSSD/          содержимое раздела LOLSSD (утилита для Linux, профили)
#   dist/windows/C/       файлы, которые кладутся в раздел Windows
#   dist/windows/lolssd.reg  применяется к кустам реестра образа офлайн
#   dist/wsl/             пусковой WSL-дистрибутив (make wsl)

GO      ?= go
GOFLAGS := -trimpath -ldflags=-s\ -w
export CGO_ENABLED := 0

WIN := dist/windows/C
# docker или podman, что найдётся
CONTAINER ?= $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null)

.PHONY: dist wsl test clean

dist:
	mkdir -p dist/LOLSSD/host dist/LOLSSD/profiles "$(WIN)/Program Files/lolssd" "$(WIN)/Windows/Setup/Scripts" dist/wsl
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o dist/LOLSSD/host/lolssd ./cmd/lolssd
	# windowsgui: задания планировщика не открывают окно консоли; журнал пишется в файл.
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w -H windowsgui" -o "$(WIN)/Program Files/lolssd/lolssd-win.exe" ./cmd/lolssd-win
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o dist/wsl/lolssd-wsl ./cmd/lolssd-wsl
	# Файлы для Windows — с CRLF: cmd и reg import на LF спотыкаются.
	for f in SetupComplete.cmd lolssd-setup.ps1; do sed 's/\r*$$/\r/' build/setup/$$f > "$(WIN)/Windows/Setup/Scripts/$$f"; done
	sed 's/\r*$$/\r/' build/setup/lolssd.reg > dist/windows/lolssd.reg
	cp README.md dist/LOLSSD/README.md
	cp build/lolssd.sh dist/LOLSSD/lolssd.sh

# Нужен docker или podman. Результат: dist/wsl/lolssd.tar для
#   wsl --import lolssd C:\ProgramData\lolssd\wsl lolssd.tar
wsl: dist
	@test -n "$(CONTAINER)" || { echo "нужен docker или podman"; exit 1; }
	cp build/wsl/Dockerfile build/wsl/wsl.conf dist/wsl/
	$(CONTAINER) build -t lolssd-wsl dist/wsl
	id=$$($(CONTAINER) create lolssd-wsl) && $(CONTAINER) export $$id > dist/wsl/lolssd.tar; s=$$?; $(CONTAINER) rm $$id >/dev/null; exit $$s
	rm dist/wsl/Dockerfile dist/wsl/wsl.conf

test:
	$(GO) vet ./...
	GOOS=windows $(GO) vet ./cmd/lolssd-win
	$(GO) test ./...

clean:
	rm -rf dist
