BINARY := go-bookshelf
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build run test vet fmt fmt-check checkweb tidy smoke docker clean

all: fmt-check vet checkweb test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

run:
	go run ./cmd/$(BINARY)

test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal ./scripts ./web

fmt-check:
	@unformatted=$$(gofmt -l ./cmd ./internal ./scripts ./web); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed on:"; echo "$$unformatted"; exit 1; fi

# Static check of the frontend: every import specifier and asset reference in
# web/dist resolves. Stands in for a bundler; needs no Node toolchain.
checkweb:
	go run ./scripts/checkweb

tidy:
	go mod tidy

# End-to-end: build, generate a synthetic library, boot the server, drive it.
smoke:
	./scripts/smoke.sh

docker:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) .

clean:
	rm -rf bin out
