# All Go tooling runs in containers; the host needs only Docker.
DEV_IMAGE  ?= peb-gateway-dev
IMAGE      ?= peb-gateway:dev
ROOT       := $(abspath ..)
DOCKER_RUN  = docker run --rm -u $(shell id -u) -v "$(ROOT)":/src -w /src/gateway \
              -v peb-gomod:/go/pkg/mod -v peb-gocache:/cache/go-build $(DEV_IMAGE)

.PHONY: dev-image test test-load fuzz build image tidy

dev-image:
	docker build --build-arg UID=$(shell id -u) -f Dockerfile.dev -t $(DEV_IMAGE) .

test: dev-image
	$(DOCKER_RUN) go test ./... -race -count=1
	$(DOCKER_RUN) go vet ./...
	@test -z "$$($(DOCKER_RUN) gofmt -l .)" || { echo "gofmt needed:"; $(DOCKER_RUN) gofmt -l .; exit 1; }

# Fuzz targets, FUZZTIME per target (default 60s)
FUZZTIME ?= 60s
# Cap on minimising each new interesting input; the default (60s) can eat the whole
# FUZZTIME while the execs counter sits at 0/sec and looks like a hang.
FUZZMIN ?= 5s
fuzz: dev-image
	$(DOCKER_RUN) go test ./internal/s3auth -run='^$$' -fuzz=FuzzVerifyHeader -fuzztime=$(FUZZTIME) -fuzzminimizetime=$(FUZZMIN)
	$(DOCKER_RUN) go test ./internal/s3auth -run='^$$' -fuzz=FuzzVerifyPresigned -fuzztime=$(FUZZTIME) -fuzzminimizetime=$(FUZZMIN)
	$(DOCKER_RUN) go test ./internal/s3auth -run='^$$' -fuzz=FuzzCanonical -fuzztime=$(FUZZTIME) -fuzzminimizetime=$(FUZZMIN)
	$(DOCKER_RUN) go test ./internal/ingest -run='^$$' -fuzz=FuzzIndexTar -fuzztime=$(FUZZTIME) -fuzzminimizetime=$(FUZZMIN)

# Load test (several GB of synthetic data, small spool, slow ingest); not part of `make test`.
test-load: dev-image
	$(DOCKER_RUN) go test -tags load -run TestLoad -timeout 30m -count=1 -v ./internal/s3api/

# Static binary at bin/peb-gateway
build: dev-image
	$(DOCKER_RUN) sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/peb-gateway ./cmd/peb-gateway'

# Production image (not pushed)
image:
	docker build -t $(IMAGE) .

tidy: dev-image
	$(DOCKER_RUN) go mod tidy
