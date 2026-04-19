BIN      := bin/rhodium
PKG      := .
GOFLAGS  ?=
LDFLAGS  ?= -s -w

.PHONY: all build test vet fmt run install clean

all: build

build: $(BIN)

$(BIN): $(shell find . -name '*.go' -not -path './bin/*')
	@mkdir -p $(dir $@)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ $(PKG)

test:
	go test $(GOFLAGS) ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

run: build
	./$(BIN)

install:
	go install $(GOFLAGS) -ldflags '$(LDFLAGS)' $(PKG)

clean:
	rm -rf bin/
