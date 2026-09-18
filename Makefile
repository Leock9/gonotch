PREFIX  ?= $(HOME)/.local
BIN     := bin
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# A Homebrew (linuxbrew) ld ahead of the system one on PATH cannot resolve the system GTK's own
# dependencies (libX11, libepoxy, freetype) and the link fails with undefined references. -B makes
# gcc take ld from /usr/bin instead; harmless where /usr/bin/ld is the only one.
export CGO_LDFLAGS := -B/usr/bin/ $(CGO_LDFLAGS)

.PHONY: all build test run install uninstall clean dist screenshots

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN)/gonotch ./cmd/gonotch
	CGO_ENABLED=0 go build -ldflags "-s -w" -o $(BIN)/gonotch-hook ./cmd/gonotch-hook

# The core has no cgo, so its tests never wait for GTK to compile
test:
	go test -race -count=1 ./internal/app/... ./internal/config/... ./internal/hooks/... ./internal/providers/... \
		./internal/server/... ./internal/sessions/... ./internal/text/... ./internal/ui/layout/... \
		./internal/usage/... ./internal/x11/...

run: build
	$(BIN)/gonotch

install: build
	install -Dm755 $(BIN)/gonotch $(PREFIX)/bin/gonotch
	install -Dm755 $(BIN)/gonotch-hook $(PREFIX)/bin/gonotch-hook

uninstall:
	rm -f $(PREFIX)/bin/gonotch $(PREFIX)/bin/gonotch-hook

clean:
	rm -rf $(BIN) $(DIST)

# Release artifacts: a tarball under a name that never changes (so releases/latest/download/ always
# finds it), a .deb, and their checksums. Build on the oldest supported Ubuntu: the binary then runs
# on every later one.
dist:
	rm -rf $(DIST) && mkdir -p $(DIST)/gonotch-linux-amd64
	go build -trimpath -ldflags "-s -w $(LDFLAGS)" -o $(DIST)/gonotch-linux-amd64/gonotch ./cmd/gonotch
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(DIST)/gonotch-linux-amd64/gonotch-hook ./cmd/gonotch-hook
	cp README.md LICENSE $(DIST)/gonotch-linux-amd64/
	tar -C $(DIST) -czf $(DIST)/gonotch-linux-amd64.tar.gz gonotch-linux-amd64
	scripts/package-deb.sh $(VERSION) $(DIST)/gonotch-linux-amd64 $(DIST)
	cd $(DIST) && sha256sum gonotch-linux-amd64.tar.gz *.deb > SHA256SUMS

# >>> claude workflow (gerado por setup-claude.sh — não editar)
# Alvos do workflow de agentes (review-static, claude-tools, claude-tools-test).
# `-include` com hífen: se o .claude/ não estiver presente, o make segue sem erro.
-include .claude/claude.mk
# <<< claude workflow

# The README's images, rendered in a container from the real drawing code (first run compiles GTK
# bindings for a while; the cache volume keeps later runs quick)
screenshots:
	docker build -t gonotch-screenshots scripts/screenshots
	docker run --rm -e HOST_UID=$$(id -u) -e HOST_GID=$$(id -g) -v gonotch-screenshots-cache:/cache \
		-v "$$PWD":/src gonotch-screenshots /src/scripts/screenshots/render.sh
