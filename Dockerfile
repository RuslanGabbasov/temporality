FROM golang:1.27-alpine AS build
ARG KERNEL_BUILD_TAGS=""
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY frp ./frp
COPY aml ./aml
COPY observation ./observation
COPY kernel ./kernel
COPY examples ./examples
RUN CGO_ENABLED=0 go build -o /out/temporality-runtime ./cmd/temporality-runtime \
 && CGO_ENABLED=0 go build -o /out/temporality-executor ./cmd/temporality-executor \
 && CGO_ENABLED=0 go build -tags "$KERNEL_BUILD_TAGS" -o /out/temporality-agent-kernel ./cmd/agent-kernel

FROM alpine:3.22 AS runtime
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-runtime /usr/local/bin/temporality-runtime
COPY migrations ./migrations
USER temporality
EXPOSE 8080
ENTRYPOINT ["temporality-runtime"]

FROM alpine:3.22 AS agent-kernel
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-agent-kernel /usr/local/bin/temporality-agent-kernel
COPY migrations ./migrations
# The optional run_command sandbox shells out to the Docker CLI against the
# host daemon socket, so the CLI must exist inside the image.
COPY --from=docker:27-cli /usr/local/bin/docker /usr/local/bin/docker
USER temporality
EXPOSE 8090
ENTRYPOINT ["temporality-agent-kernel"]

FROM alpine:3.22 AS executor
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-executor /usr/local/bin/temporality-executor
USER temporality
ENTRYPOINT ["temporality-executor"]
