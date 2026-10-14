FROM golang:1.27-alpine AS build
ARG KERNEL_BUILD_TAGS=""
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
    CGO_ENABLED=0 go build -o /out/temporality-journal ./cmd/temporality-journal \
 && CGO_ENABLED=0 go build -tags "$KERNEL_BUILD_TAGS" -o /out/temporality-agent-kernel ./cmd/agent-kernel \
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
WORKDIR /app
COPY --from=build /out/temporality-agent-kernel /usr/local/bin/temporality-agent-kernel
COPY --from=build /out/test-mcp /usr/local/bin/test-mcp
# The kernel applies the kernel event outbox migration (000019) from the
# filesystem before serving traffic.
COPY migrations ./migrations
# The optional run_command sandbox shells out to the Docker CLI against the
# host daemon socket, so the CLI must exist inside the image.
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["temporality-agent-kernel"]
EXPOSE 8090
