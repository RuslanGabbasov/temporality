FROM golang:1.27-alpine
# curl: agents fetch URLs without falling back to hand-written python
# scripts; jq: idiomatic JSON handling for HTTP API responses.
# bash: model-authored heredocs and scripts assume a POSIX-plus shell — plain
# ash breaks them in subtle ways.
# python3: the scripting runtime agents reach for most; its absence forced
# byte-by-byte file surgery via sh.
# ripgrep: fast code search without fragile grep -r invocations.
RUN apk add --no-cache git openssh-client ca-certificates curl jq bash python3 ripgrep
