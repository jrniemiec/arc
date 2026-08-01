GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X main.version=$(VERSION)
BINARY   := arc
INSTALL  := $(HOME)/dev/bin/$(BINARY)
BINDIR   ?= $(HOME)/dev/bin
SCRIPTS  := $(wildcard scripts/*.sh)
SCRIPT_BINS := $(patsubst scripts/%.sh,$(BINDIR)/%,$(SCRIPTS))

.PHONY: build install install-scripts run test fmt vet clean feedprobe dist release

build:
	@mkdir -p bin
	$(GO) build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

install: build install-scripts
	ln -sf $(CURDIR)/bin/$(BINARY) $(INSTALL)
	@echo "Installed: $(INSTALL)"

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
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

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
