PREFIX ?= $(HOME)/.local
BIN    := bin

# A Homebrew (linuxbrew) ld ahead of the system one on PATH cannot resolve the system GTK's own
# dependencies (libX11, libepoxy, freetype) and the link fails with undefined references. -B makes
# gcc take ld from /usr/bin instead; harmless where /usr/bin/ld is the only one.
export CGO_LDFLAGS := -B/usr/bin/ $(CGO_LDFLAGS)

.PHONY: all build test run install uninstall clean

all: build

build:
	go build -o $(BIN)/gonotch ./cmd/gonotch
	CGO_ENABLED=0 go build -o $(BIN)/gonotch-hook ./cmd/gonotch-hook

# The core has no cgo, so its tests never wait for GTK to compile
test:
	go test ./internal/app/... ./internal/config/... ./internal/hooks/... ./internal/providers/... \
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
	rm -rf $(BIN)
