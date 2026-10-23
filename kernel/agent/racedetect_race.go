//go:build race

package agent

// raceEnabled reports whether the race detector is linked into this build.
// temporal sdk v1.46.0 has an internal workflow-channel data race
// (channelImpl.Close vs removeReceiveCallback) that surfaces only under
// -race; tests that trip it skip via this flag — see
// TestAgentRunPlanAbortOnFailure and
// docs/reports/2026-10-23-kernel-agent-race-fix-dev.md.
const raceEnabled = true
