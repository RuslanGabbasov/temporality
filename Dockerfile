FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG KERNEL_BUILD_TAGS=""
# BuildVersion: stamped by the release workflow (calendar version); "dev" in
# local builds. Lands in the binaries via -ldflags and shows up in the start
# log and /healthz.
ARG BUILD_VERSION="dev"
# Multi-arch releases (linux/amd64 + linux/arm64): the build stage always runs
# on the builder's native platform and cross-compiles for the target — full
# QEMU emulation of a Go build would take 10x longer for the same output.
ARG TARGETARCH
ENV GOARCH=$TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
# Cache mounts (BuildKit) keep downloaded modules and compiled package
# artifacts across builds: without them every source change recompiles the
# whole dependency tree from scratch — minutes on a laptop, 10+ on a server.
# sharing=locked guards the parallel journal/agent-kernel target builds.
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download
COPY cmd ./cmd
COPY controlplane ./controlplane
COPY observation ./observation
COPY kernel ./kernel
COPY skills ./skills
COPY workspace ./workspace
COPY examples ./examples
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 go build -ldflags "-X main.buildVersion=${BUILD_VERSION}" -o /out/temporality-journal ./cmd/temporality-journal \
 && CGO_ENABLED=0 go build -tags "$KERNEL_BUILD_TAGS" -ldflags "-X main.buildVersion=${BUILD_VERSION}" -o /out/temporality-agent-kernel ./cmd/agent-kernel \
 && CGO_ENABLED=0 go build -o /out/test-mcp ./examples/test-mcp

FROM alpine:3.22 AS journal
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-journal /usr/local/bin/temporality-journal
USER temporality
EXPOSE 8080
ENTRYPOINT ["temporality-journal"]

FROM alpine:3.22 AS agent-kernel
# setpriv: the entrypoint starts as root, picks up the Docker socket's group
# (root:docker on Linux hosts, root:root on Docker Desktop/OrbStack proxies)
# and drops to the unprivileged user with that group attached — no per-host
# GID configuration in .env.
RUN adduser -D -u 10001 temporality && apk add --no-cache util-linux
# In-process embedding model cache (semantic knowledge layer): the named
# volume mounts here and inherits this ownership from the image.
RUN mkdir -p /var/cache/rembed && chown temporality /var/cache/rembed
WORKDIR /app
COPY --from=build /out/temporality-agent-kernel /usr/local/bin/temporality-agent-kernel
COPY --from=build /out/test-mcp /usr/local/bin/test-mcp
# The kernel applies the kernel event outbox migration (000019) from the
# filesystem before serving traffic.
COPY migrations ./migrations
# The optional run_command sandbox shells out to the Docker CLI against the
# host daemon socket, so the CLI must exist inside the image. The buildx
# plugin rides along: without it `docker build` falls back to the deprecated
# legacy builder (loud WARNING on every sandbox/profile image build).
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker/buildx-bin:0.17.1 /buildx /usr/local/lib/docker/cli-plugins/docker-buildx
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["temporality-agent-kernel"]
EXPOSE 8090
