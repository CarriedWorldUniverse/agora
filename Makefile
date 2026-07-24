# VERSION identifies the build. The fallback chain matters: this repo is
# cairn-managed, so there is no .git in a working folder and `git describe`
# fails — which silently stamped every local build "dev" and made it
# impossible to tell one installed binary apart from another. cairn is
# asked first, git second (for a plain clone or CI checkout), "dev" last.
VERSION ?= $(shell cairn version 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/CarriedWorldUniverse/agora/internal/version.Version=$(VERSION)

# Where `make install` puts the binary. ~/.local/bin is the default because
# it is already on PATH for the operator and needs no privileges.
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build test vet version clean install uninstall installed

build:
	go build -ldflags '$(LDFLAGS)' -o bin/agora ./cmd/agora

test:
	go test -race ./...

vet:
	go vet ./...

version:
	@echo $(VERSION)

clean:
	rm -rf bin/

# install builds and replaces the binary on PATH. Gated on vet+test: an
# installed agora is the harness the operator actually works in, so
# shipping one that does not pass its own suite is not a tradeoff worth
# offering. Bypass deliberately with `make build && cp bin/agora ...`.
install: vet test build
	@mkdir -p $(BINDIR)
	install -m 0755 bin/agora $(BINDIR)/agora
	@echo "installed $(BINDIR)/agora — $$($(BINDIR)/agora -version)"

uninstall:
	rm -f $(BINDIR)/agora

# installed reports the version on PATH next to the version in this tree,
# so the gap between "merged" and "actually running" is one command away
# rather than something discovered by accident.
installed:
	@echo "on PATH:    $$(command -v agora >/dev/null 2>&1 && agora -version || echo '<not installed>')"
	@echo "this tree:  agora $(VERSION)"
