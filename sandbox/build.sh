#!/bin/sh
# Rebuild the agent sandbox image and pin its digest in .env.
#
# The kernel runs the sandbox with --pull=never and a sha256-pinned reference
# (docs/sandbox-security-matrix.md). That means the local docker daemon must
# hold the exact image the digest points to: after changing sandbox/Dockerfile
# or after `docker system prune` the old digest disappears and run_command
# fails with exit 125 "No such image". Run this script to rebuild and re-pin:
#
#   ./sandbox/build.sh
#
# It also recreates the agent-kernel container when compose is up, because the
# image reference is passed to it through the environment.
set -eu

cd "$(dirname "$0")/.."

TAG=temporality-sandbox:latest
docker build -t "$TAG" sandbox/

DIGEST="$(docker image inspect --format '{{.Id}}' "$TAG")"
REF="temporality-sandbox@${DIGEST}"

if [ -f .env ] && grep -q '^KERNEL_SANDBOX_IMAGE=' .env; then
	sed -i.bak "s|^KERNEL_SANDBOX_IMAGE=.*|KERNEL_SANDBOX_IMAGE=${REF}|" .env && rm -f .env.bak
else
	echo "KERNEL_SANDBOX_IMAGE=${REF}" >>.env
fi
echo "pinned ${REF}"

# Prove the digest reference resolves in the local store before restarting
# anything (--pull=never is exactly how the kernel will run it).
docker run --rm --pull=never "$REF" true

if docker compose ps --status running 2>/dev/null | grep -q agent-kernel; then
	docker compose up -d agent-kernel
	echo "agent-kernel recreated with the new image reference"
else
	echo "agent-kernel is not running; start the stack when needed"
fi
