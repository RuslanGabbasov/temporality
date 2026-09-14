FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY frp ./frp
RUN CGO_ENABLED=0 go build -o /out/temporality-runtime ./cmd/temporality-runtime \
 && CGO_ENABLED=0 go build -o /out/temporality-executor ./cmd/temporality-executor

FROM alpine:3.22 AS runtime
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-runtime /usr/local/bin/temporality-runtime
COPY migrations ./migrations
USER temporality
EXPOSE 8080
ENTRYPOINT ["temporality-runtime"]

FROM alpine:3.22 AS executor
RUN adduser -D -u 10001 temporality
WORKDIR /app
COPY --from=build /out/temporality-executor /usr/local/bin/temporality-executor
USER temporality
ENTRYPOINT ["temporality-executor"]
