# All Go tooling runs in containers; the host needs only Docker.
DEV_IMAGE  ?= peb-gateway-dev
IMAGE      ?= peb-gateway:dev
ROOT       := $(abspath ..)
DOCKER_RUN  = docker run --rm -u $(shell id -u) -v "$(ROOT)":/src -w /src/gateway \
              -v peb-gomod:/go/pkg/mod -v peb-gocache:/cache/go-build $(DEV_IMAGE)

.PHONY: dev-image test vet fmt build image tidy

dev-image:
	docker build --build-arg UID=$(shell id -u) -f Dockerfile.dev -t $(DEV_IMAGE) .

test: dev-image
	$(DOCKER_RUN) go test ./... -race -count=1
	$(DOCKER_RUN) go vet ./...
	@test -z "$$($(DOCKER_RUN) gofmt -l .)" || { echo "gofmt needed:"; $(DOCKER_RUN) gofmt -l .; exit 1; }

# Static binary at bin/peb-gateway
build: dev-image
	$(DOCKER_RUN) sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/peb-gateway ./cmd/peb-gateway'

# Production image (not pushed)
image:
	docker build -t $(IMAGE) .

tidy: dev-image
	$(DOCKER_RUN) go mod tidy
