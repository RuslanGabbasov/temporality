package main

import (
	"os"
	"testing"
	"time"

	"github.com/temporality-project/temporality/kernel/agent"
)

// TestMain shrinks the matrix inbound collection window: production waits a
// couple of seconds for the tail of a multi-file message before releasing a
// (chat, sender) group, which would add wall-clock time to every cycle-based
// test without changing what they assert.
func TestMain(m *testing.M) {
	agent.SetMatrixBatchTimingForTests(20*time.Millisecond, 60*time.Millisecond, 5*time.Millisecond)
	os.Exit(m.Run())
}
