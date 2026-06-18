GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X main.version=$(VERSION)
BINARY   := arc
INSTALL  := $(HOME)/dev/bin/$(BINARY)
BINDIR   ?= $(HOME)/dev/bin
SCRIPTS  := $(wildcard scripts/*.sh)
SCRIPT_BINS := $(patsubst scripts/%.sh,$(BINDIR)/%,$(SCRIPTS))

.PHONY: build install install-scripts run test fmt vet clean feedprobe

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

clean:
	rm -rf bin/
