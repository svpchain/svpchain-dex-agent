#!/usr/bin/make -f

VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

# Match the version strings the binary already imports transitively from
# cosmos-sdk (mirrors cmd/svpchain-dex-agent/Dockerfile).
ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=svpchain \
	-X github.com/cosmos/cosmos-sdk/version.AppName=svpchain-dex-agent \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS := -ldflags '$(ldflags)'

.PHONY: build install test cover vet fmt vendor docker deploy clean

# Build the agent binary into ./build. Uses the go.mod replace directive
# pointing at the sibling protocol checkout (../svpagent/protocol).
build:
	go build -mod=readonly $(BUILD_FLAGS) -o build/svpchain-dex-agent ./cmd/svpchain-dex-agent

install:
	go install -mod=readonly $(BUILD_FLAGS) ./cmd/svpchain-dex-agent

test:
	go test ./...

# Per-package coverage plus the total; profile lands in build/cover.out
# (inspect with `go tool cover -html=build/cover.out`).
cover:
	@mkdir -p build
	go test -coverprofile=build/cover.out ./...
	go tool cover -func=build/cover.out | tail -1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Materialize dependencies into ./vendor so the Docker image can build without
# the sibling protocol checkout. Run before `docker`.
vendor:
	go mod vendor

# Build the deployable image. Vendors first so the build context is
# self-contained (the replace target is not inside the Docker context).
docker: vendor
	docker build --platform linux/amd64 \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		-t svpchain-dex-agent:$(VERSION) \
		-f cmd/svpchain-dex-agent/Dockerfile .

# Deploy to a remote host (wraps scripts/dex-agent-deploy.sh, which builds
# its own image). Pass flags via DEPLOY_FLAGS:
#   make deploy DEPLOY_FLAGS="--host www@svpdev1.example.com"
deploy:
	./scripts/dex-agent-deploy.sh $(DEPLOY_FLAGS)

clean:
	rm -rf build/ vendor/
