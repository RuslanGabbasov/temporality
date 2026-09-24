# Approval model (Phase 2)

## Contract

`approval.requested` is written to the durable Kernel outbox before the workflow waits for a human. Its `data.operation` contains:

- `id`: the stable `operation_id` used by approval and subsequent tool events;
- `type`, `summary`;
- `arguments`: a bounded display projection with sensitive keys/values redacted;
- `arguments_hash`: `sha256:` plus the digest of canonical JSON for the full arguments.

`data.risk` is an explicit advisory level. `data.redaction` reports whether any fields were redacted and whether output was truncated. `data.details` contains only non-sensitive display metadata such as tool/workspace policy. Full canonical arguments are not copied into Temporality events.

An approval signal must provide `operation_id`, `arguments_hash`, `actor_id`, and decision. The workflow records `approval.granted` with the same operation ID/hash and starts `tool.started` only after an exact hash match. A mismatch is recorded as rejection and cannot execute the tool. `tool.started`, `tool.completed/failed`, and MCP lifecycle events carry the operation ID and arguments hash. All events include the same run/frame scope and causal parent links through the envelope metadata in `data`.

## Redaction and limits

Display data is limited to 4 KiB overall, 256 bytes per string, 32 members per collection, and six nested levels. Sensitive names include token, password, secret, authorization, credential and private key variants. Bearer values and URL user-info are redacted. Truncation and redaction are visible. The digest deliberately identifies the unredacted canonical arguments; it does not reveal them directly, though it may permit guessing low-entropy arguments.

The digest is an identity check, not proof that the UI is trustworthy or that the operation is safe. The approval UI must display the redacted `operation` and submit its exact hash. Tool policy and sandbox restrictions remain independent controls.

## Sandbox auto-approval

When `Activities.PrepareRun` confirms that the Docker sandbox is configured, it assigns `run_command` to the workflow's auto-approval policy and removes it from the human approval list. The deterministic workflow emits `approval.auto_granted` before `tool.started`; the event includes the bounded operation display, canonical arguments hash, workspace path, and `policy_id: sandbox.workspace.v1`. The sandbox still enforces its container, workspace, network, and resource settings. This policy cannot auto-approve MCP tools or the explicit `request_approval` tool. API callers cannot choose the policy: `PrepareRun` replaces the policy fields from server configuration.

## Validation evidence

`TestApprovedToolOperationIsVisibleAndHashBound` checks `approval.requested → approval.granted → tool.started → tool.completed`, common run/frame/operation/hash, causal links, secret absence and denial when the signal hash differs. `TestSandboxAutoApprovalIsRecordedAndRunsWithoutHumanSignal` checks the sandbox policy event/order and verifies that it does not create a pending human request. `TestApprovalOperationHashesCanonicalArgsAndRedactsDisplay` checks canonical map-order stability and redaction. These are workflow-level tests; a human clicking the live UI against a real MCP write remains a separate acceptance test.
