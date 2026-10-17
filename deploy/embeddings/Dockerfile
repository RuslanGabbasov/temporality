# rembed: pure-Go OpenAI-compatible embeddings server (docs/supervisor-
# model.md §6). One static binary, no cgo, no ONNX Runtime; serves
# intfloat/multilingual-e5-small (384-dim, 100+ languages, ~120MB fp32
# weights) validated by the project's golden matrix. The model downloads
# from the Hugging Face Hub on first start into the cached volume.
FROM golang:1.27-alpine AS build
ARG REMBED_REF=main
RUN apk add --no-cache git
WORKDIR /src
RUN git clone --depth 1 --branch "${REMBED_REF}" https://github.com/rostamlabs/rembed.git . \
 && CGO_ENABLED=0 go build -o /out/rembed ./cmd/rembed

FROM alpine:3.22
RUN adduser -D -u 10001 temporality \
 && mkdir -p /cache/rembed && chown -R temporality /cache
COPY --from=build /out/rembed /usr/local/bin/rembed
USER temporality
ENV REMBED_CACHE=/cache/rembed
WORKDIR /cache
EXPOSE 8099
ENTRYPOINT ["rembed", "serve", "-model", "intfloat/multilingual-e5-small", "-addr", ":8099", "-int8"]
