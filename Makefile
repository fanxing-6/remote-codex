.PHONY: build build-windows build-web test phase0 build-desktop package-mac package-windows
export TMPDIR := $(CURDIR)/.work/tmp
build: build-web
	mkdir -p bin
	go build -trimpath -o bin/remote-codex ./cmd/remote-codex
build-windows: build-web
	mkdir -p bin
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/remote-codex-windows-amd64.exe ./cmd/remote-codex
build-web:
	mkdir -p .work/tmp
	cd web && npm ci --cache ../.work/npm-cache --prefer-offline && npm run build
test:
	mkdir -p .work/tmp
	go test -race ./...
build-desktop: build
	cd desktop && npm ci --cache ../.work/npm-cache --prefer-offline && npm run typecheck && npm run build
package-mac: build-desktop
	cd desktop && npm run package:mac
package-windows: build-windows
	cd desktop && npm ci --cache ../.work/npm-cache --prefer-offline && npm run package:windows
phase0:
	mkdir -p .work/tmp
	web/node_modules/.bin/esbuild web/shim/phase0-shim.ts --bundle --platform=node --format=cjs --external:electron --external:bufferutil --external:utf-8-validate --outfile=.work/phase0-shim.cjs
	$(or $(BOOTSTRAP_PYTHON),.venv/bin/python) tests/contract/bootstrap.py .work/phase0-shim.cjs
