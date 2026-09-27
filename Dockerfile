FROM golang:1.27-alpine AS build
ARG KERNEL_BUILD_TAGS=""
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY controlplane ./controlplane
COPY observation ./observation
COPY kernel ./kernel
COPY workspace ./workspace
COPY examples ./examples
RUN CGO_ENABLED=0 go build -o /out/temporality-journal ./cmd/temporality-journal \
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
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-agent-kernel /usr/local/bin/temporality-agent-kernel
COPY --from=build /out/test-mcp /usr/local/bin/test-mcp
# The kernel applies the kernel event outbox migration (000019) from the
# filesystem before serving traffic.
COPY migrations ./migrations
# The optional run_command sandbox shells out to the Docker CLI against the
# host daemon socket, so the CLI must exist inside the image.
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
USER temporality
EXPOSE 8090
ENTRYPOINT ["temporality-agent-kernel"]
