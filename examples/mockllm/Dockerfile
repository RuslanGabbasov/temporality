# Multi-stage-free minimal build: the mock is a single Go file with zero
# dependencies beyond the standard library, so the module root is the only
# context it needs (go.mod + the package directory).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked go mod download
COPY examples/mockllm ./examples/mockllm
RUN CGO_ENABLED=0 go build -o /out/mockllm ./examples/mockllm

FROM alpine:3.22
RUN adduser -D -u 10001 mockllm
COPY --from=build /out/mockllm /usr/local/bin/mockllm
USER mockllm
EXPOSE 8081
ENTRYPOINT ["mockllm"]
