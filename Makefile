GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARY   := arc
INSTALL  := $(HOME)/dev/bin/$(BINARY)
BINDIR   ?= $(HOME)/dev/bin
SCRIPTS  := $(wildcard scripts/*.sh)
SCRIPT_BINS := $(patsubst scripts/%.sh,$(BINDIR)/%,$(SCRIPTS))

# Optional build tags, empty by default so a clone builds the shipping binary.
#
# Makefile.local is gitignored and sets TAGS for one machine, so 'make install'
# there produces the personal build without anyone having to remember a second
# target. Override with 'make install TAGS=' to get the plain binary anyway.
TAGS ?=
-include Makefile.local

# The tag is stamped into --version because both builds produce bin/arc, and
# without it there is no way to tell which one is installed.
# go build -tags takes a comma-separated list, so TAGS goes through verbatim.
TAGFLAGS := $(if $(TAGS),-tags $(TAGS),)
LDFLAGS  ?= -X main.version=$(VERSION)$(if $(TAGS),+$(TAGS),)

.PHONY: build build-sessions install install-sessions install-scripts run test test-sessions fmt vet clean feedprobe dist release

build:
	@mkdir -p bin
	$(GO) build $(TAGFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

# Explicit personal build, for when TAGS is not set locally.
build-sessions:
	@$(MAKE) build TAGS=arcsessions

install: build install-scripts
	ln -sf $(CURDIR)/bin/$(BINARY) $(INSTALL)
	@echo "Installed: $(INSTALL) $(if $(TAGS),($(TAGS)),)"

install-sessions:
	@$(MAKE) install TAGS=arcsessions

# Always runs the tagged suite, whatever TAGS is set to locally.
test-sessions:
	$(GO) test -tags arcsessions ./...

install-scripts:
	@mkdir -p $(BINDIR)
	@for s in $(SCRIPTS); do \
	  name=$$(basename $$s .sh); \
	  chmod +x $$s; \
	  ln -sf $(CURDIR)/$$s $(BINDIR)/$$name; \
	  echo "Installed: $(BINDIR)/$$name"; \
	done

run: build
	bin/$(BINARY)

test:
	$(GO) test $(TAGFLAGS) ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet $(TAGFLAGS) ./...

feedprobe:
	@mkdir -p bin
	$(GO) build -o bin/feedprobe ./cmd/feedprobe

dist: ## Build release tarballs for darwin arm64+amd64 (usage: make dist VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then echo "usage: make dist VERSION=x.y.z"; exit 1; fi
	@mkdir -p dist/arm64 dist/amd64
	GOOS=darwin GOARCH=arm64 $(GO) build -ldflags '-s -w -X main.version=$(VERSION)' -o dist/arm64/arc .
	GOOS=darwin GOARCH=amd64 $(GO) build -ldflags '-s -w -X main.version=$(VERSION)' -o dist/amd64/arc .
	tar -czf dist/arc_$(VERSION)_darwin_arm64.tar.gz -C dist/arm64 arc
	tar -czf dist/arc_$(VERSION)_darwin_amd64.tar.gz -C dist/amd64 arc
	@rm -rf dist/arm64 dist/amd64
	@echo "    OK: dist/arc_$(VERSION)_darwin_arm64.tar.gz"
	@echo "    OK: dist/arc_$(VERSION)_darwin_amd64.tar.gz"

release: ## Tag and push a release (usage: make release VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then echo "usage: make release VERSION=x.y.z"; exit 1; fi
	git tag v$(VERSION)
	git push origin v$(VERSION)

clean:
	rm -rf bin/ dist/
