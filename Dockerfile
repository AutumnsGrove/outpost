# syntax=docker/dockerfile:1
#
# Outpost image. Built by GitHub Actions for linux/amd64 and linux/arm64 (the
# potato is arm64) and published to ghcr.io/autumnsgrove/outpost. The potato only
# ever pulls a finished image; it never builds one.
#
# Same shape as Warbler's and Polaris's Dockerfiles: both build stages are pinned
# to the build machine's own architecture so nothing runs under emulation (the
# frontend is plain JS/CSS/HTML, and Go cross-compiles natively), and only the
# tiny final stage is assembled per target.

# --- web build ---------------------------------------------------------
# web/build is not committed (PLAN 38), so this stage builds it from web/src
# every time and the Go stage below copies the result in. `go:embed all:build`
# needs a directory that exists, which is why the copy is not optional and why
# the order is web first.
FROM --platform=$BUILDPLATFORM node:22-bookworm AS web-build
WORKDIR /web
# Only the manifests, so a source-only change does not reinstall node_modules.
COPY web/package.json web/pnpm-lock.yaml ./
RUN corepack enable && corepack prepare pnpm@10.32.1 --activate && \
    pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm run build

# --- Go build ----------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /web/build ./web/build

# The commit is passed in by CI so the page's build stamp survives: a container
# has no .git for debug.ReadBuildInfo to read, and every deployed build would
# otherwise call itself "dev". Note the dot before buildVersion: the linker splits
# `importpath.name`, and the slash form is rejected outright.
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
# CGO_ENABLED=0: nothing here needs a C library (SQLite is modernc's pure-Go
# build), so the binary is fully static and cross-compiles with GOOS/GOARCH alone.
RUN --mount=type=cache,target=/root/.cache/go-build,id=outpost-go-${TARGETOS}-${TARGETARCH} \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X outpost/internal/api.buildVersion=${VERSION}" \
      -o /out/outpost ./cmd/outpost

# --- runtime -----------------------------------------------------------
# Distroless: the program, CA certificates and nothing else. No shell, no
# package manager, no tailscale CLI — internal/tailnet reads tailscaled's LocalAPI
# over the mounted socket instead of executing a binary (PLAN 38).
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/AutumnsGrove/outpost" \
      org.opencontainers.image.description="Outpost: a Minecraft server that only exists while somebody is playing" \
      org.opencontainers.image.title="outpost"

COPY --from=go-build /out/outpost /app/outpost

# The data directory, also the working directory: config.yaml, outpost.db, packs/
# and the .mrpack cache are all relative paths from here, so the deployed config
# is the same file the repo documents and compose only has to mount one folder.
WORKDIR /data

# Runs as uid 1000 so the files it writes belong to the same user that owns the
# bind mounts on the potato, exactly like the sibling images.
USER 1000:1000
EXPOSE 8896

# The binary asking itself: a distroless image has no curl, and "the process is
# alive" is not the question — a daemon whose listener is dead but whose tick loop
# is still running would look healthy for ever. The update watcher reads this state
# before deciding to keep a new image or put the old one back.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD ["/app/outpost", "healthcheck"]

ENTRYPOINT ["/app/outpost"]
CMD ["serve"]
