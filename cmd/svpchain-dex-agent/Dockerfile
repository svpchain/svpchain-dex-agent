# Dockerfile for the svpchain DEX agent (cmd/svpchain-dex-agent).
#
# The resulting image contains only the agent binary plus ca-certificates.
# Runtime config lives in agent.toml, which the deployer mounts read-only at
# /etc/svpchain-dex-agent/agent.toml (see scripts/dex-agent-deploy.sh); the
# optional operator key is mounted beside it.
#
# Build from the repo root. The image builds from a VENDORED dependency tree
# (`./vendor`) so the build context is self-contained: this repo depends on
# the protocol module via a go.mod `replace` to a sibling checkout that is NOT
# inside the Docker context, so a non-vendored build could not resolve it.
# Vendoring also removes the need for GOPRIVATE credentials in the builder.
#
# Vendor first, then build:
#   go mod vendor
#   docker build --platform linux/amd64 \
#     -t svpchain-dex-agent:<tag> \
#     -f cmd/svpchain-dex-agent/Dockerfile .
#
# --platform linux/amd64 is required when building on Apple Silicon for a
# linux/amd64 remote; see scripts/dex-agent-deploy.sh for the deploy flow.

# NB: this module is go 1.25 (the protocol dependency tree is 1.23 and
# coexists fine). Pin a digest with
# `docker buildx imagetools inspect golang:1.25-alpine` if reproducibility
# across builders matters.
FROM golang:1.25-alpine AS builder

RUN set -eux; apk add --no-cache ca-certificates build-base; apk add git linux-headers bash binutils-gold

WORKDIR /svpchain

# Fully vendored (see header): all dependencies — including the protocol
# module resolved through go.mod's replace — arrive via `./vendor`, so there
# is no `go mod download` step and no network fetch.
COPY . .

# VERSION/COMMIT declared here, not at the top of the stage: BuildKit folds
# in-scope ARG values into the cache key of every subsequent RUN, so declaring
# early would bust the apk-add layer on every commit.
ARG VERSION
ARG COMMIT

# Same -tags / -ldflags pattern as the svpchain-mcp image (static linking via
# musl) so the binary runs on the alpine runner without dynamic deps.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/root/go/pkg/mod \
    go build \
      -mod=vendor \
      -tags "netgo,muslc" \
      -ldflags "-X github.com/cosmos/cosmos-sdk/version.Name=svpchain \
              -X github.com/cosmos/cosmos-sdk/version.AppName=svpchain-dex-agent \
              -X github.com/cosmos/cosmos-sdk/version.Version=$VERSION \
              -X github.com/cosmos/cosmos-sdk/version.Commit=$COMMIT \
              -X github.com/cosmos/cosmos-sdk/version.BuildTags='netgo,muslc' \
              -w -s -linkmode=external -extldflags '-Wl,-z,muldefs -static'" \
      -trimpath \
      -o /svpchain/build/svpchain-dex-agent \
      ./cmd/svpchain-dex-agent

# --------------------------------------------------------
# Runner
# --------------------------------------------------------

FROM alpine:3.21

RUN apk add --no-cache bash ca-certificates

COPY --from=builder /svpchain/build/svpchain-dex-agent /bin/svpchain-dex-agent

# Auth state (nonces / dynamic tenants / session bearers / withdraw ledger)
# is in-memory; the writable volume at /var/lib/svpchain-dex-agent holds only
# the transfer-out cap file, and the config dir holds the read-only
# agent.toml (+ operator.key when delegated execution is enabled).

# Default A2A listen port. Override via agent.toml listen_addr. The deploy
# script runs the container with --network host, so this EXPOSE is
# informational only.
EXPOSE 8081

ENTRYPOINT ["/bin/svpchain-dex-agent"]
CMD ["-config", "/etc/svpchain-dex-agent/agent.toml"]
