# ADR-007: Replaceable sandbox interface

Implementation note (2026-09-24): an opt-in Docker CLI runner now exists under `kernel/sandbox`. It pins images by digest, scopes bind mounts to a configured workspace root, disables network, drops Linux capabilities, enables `no-new-privileges`, makes the container root read-only, disables swap, sets process/CPU/memory/file limits, runs as the Kernel UID, bounds captured output and enforces forced container cleanup. Reviewer/QA can mount the workspace read-only. The live Docker smoke test passed with these restrictions and verified removal. Run Kernel with a rootless Docker daemon where available; daemon access still carries host-level risk. This is not adversarial security validation or a production security-boundary claim.

## Context

Tools need filesystem and command execution, but host processes are not an isolation boundary and Daytona should not be mandatory.

## Decision

Define `Create`, `Exec`, `Read`, `Write`, `Destroy` behind a backend interface. Development may use a local backend with an explicit unsafe label. Untrusted/model-authored code requires Docker with resource/mount/network controls, or a remote isolated backend; failure to start the selected isolated backend must fail closed.

## Alternatives

Direct host shell; hard-code Docker or Daytona into Kernel APIs; Kubernetes before deployment requires it.

## Consequences

Capabilities can be swapped without changing workflows. Docker-daemon access is privileged and must be isolated; this ADR does not claim containers are a complete defense against hostile code.
