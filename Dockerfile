# Build the manager binary
# Override BASE_IMAGE to build from another registry, e.g. docker.io/library/golang:1.26
ARG BASE_IMAGE=golang:1.26
FROM ${BASE_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o manager cmd/main.go

# --- pass-cli -----------------------------------------------------------------
# Version, URL and checksum are mandatory build args (no defaults, so a build can
# never silently pick an unverified binary). Take them per architecture from
# Proton's versions.json for the pinned release; see hack/pass-cli-versions.md.
FROM debian:bookworm-slim AS pass-cli
ARG TARGETARCH
ARG PASS_CLI_VERSION
ARG PASS_CLI_URL_AMD64
ARG PASS_CLI_SHA256_AMD64
ARG PASS_CLI_URL_ARM64
ARG PASS_CLI_SHA256_ARM64
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    test -n "${PASS_CLI_VERSION}" || { echo "PASS_CLI_VERSION is required" >&2; exit 1; }; \
    if [ "${TARGETARCH:-amd64}" = "arm64" ]; then url="${PASS_CLI_URL_ARM64}"; sum="${PASS_CLI_SHA256_ARM64}"; \
    else url="${PASS_CLI_URL_AMD64}"; sum="${PASS_CLI_SHA256_AMD64}"; fi; \
    test -n "$url" && test -n "$sum" || { echo "pass-cli URL and SHA-256 are required for this arch" >&2; exit 1; }; \
    curl -fsSL -o /usr/local/bin/pass-cli "$url"; \
    echo "${sum}  /usr/local/bin/pass-cli" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/pass-cli

# Standalone image: used by the chart as an initContainer when passCli.image is
# overridden, so the CLI version can change without rebuilding the operator.
FROM debian:bookworm-slim AS pass-cli-image
ARG PASS_CLI_VERSION
LABEL org.opencontainers.image.title="pass-cli" org.opencontainers.image.version="${PASS_CLI_VERSION}"
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=pass-cli /usr/local/bin/pass-cli /usr/local/bin/pass-cli
USER 65532:65532

# --- operator without pass-cli (dev / e2e) ----------------------------------------
# Used by `make docker-build` (and therefore the kind e2e), which never talks to Proton.
FROM debian:bookworm-slim AS manager-dev
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /
COPY --from=builder /workspace/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]

# --- operator (release target; last stage = default for plain `docker build`) -----
# pass-cli is probably dynamically linked, so a glibc base is used rather than distroless/static.
FROM debian:bookworm-slim AS manager
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /
COPY --from=builder /workspace/manager /manager
COPY --from=pass-cli /usr/local/bin/pass-cli /usr/local/bin/pass-cli
USER 65532:65532
ENTRYPOINT ["/manager"]
