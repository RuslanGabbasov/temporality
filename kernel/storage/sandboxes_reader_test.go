package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// End-to-end read over a fake docker binary: the ps subcommand lists a
// temporality-sandbox container with labels, stats reports live numbers —
// ReadSandboxContainers must stitch them into one entry.

const fakeDockerPs = `{"Command":"\"sleep 30\"","CreatedAt":"2026-10-16 10:00:00 +0500 +05","ID":"abc123","Image":"temporality-sandbox:latest","Labels":"temporality.project=omni,temporality.run=untitled-20261016,temporality.agent=lead,temporality.actor=ivan","LocalVolumes":"0","Names":"temporality-sandbox-abc123","Networks":"bridge","Ports":"","RunningFor":"About a minute","Size":"0B","State":"running","Status":"Up About a minute"}`

const fakeDockerStats = `{"BlockIO":"1.5MB / 0B","CPUPerc":"12.30%","MemPerc":"5.40%","MemUsage":"56.7MiB / 1GiB","Name":"temporality-sandbox-abc123","NetIO":"1.2kB / 45kB"}`

func TestReadSandboxContainersStitchesPsAndStats(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"ps\" ]; then\n  echo '" + fakeDockerPs + "'\nelse\n  echo '" + fakeDockerStats + "'\nfi\n"
	binary := filepath.Join(dir, "docker")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	containers := ReadSandboxContainers(context.Background(), binary)
	if len(containers) != 1 {
		t.Fatalf("containers = %+v, want 1", containers)
	}
	c := containers[0]
	if c.Name != "temporality-sandbox-abc123" || c.Image != "temporality-sandbox:latest" {
		t.Errorf("name/image = %q/%q", c.Name, c.Image)
	}
	if c.CPUPercent != 12.30 || c.MemPercent != 5.40 {
		t.Errorf("cpu/mem = %v/%v", c.CPUPercent, c.MemPercent)
	}
	if c.MemUsedBytes != 59454259 || c.MemLimit != 1073741824 {
		t.Errorf("mem bytes = %d/%d", c.MemUsedBytes, c.MemLimit)
	}
	if c.NetInBytes != 1200 || c.NetOutBytes != 45000 {
		t.Errorf("net = %d/%d", c.NetInBytes, c.NetOutBytes)
	}
	if c.Labels["temporality.project"] != "omni" || c.Labels["temporality.run"] != "untitled-20261016" || c.Labels["temporality.actor"] != "ivan" {
		t.Errorf("labels = %v", c.Labels)
	}
}

func TestReadSandboxContainersEmptyWhenNoSandboxes(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	binary := filepath.Join(dir, "docker")
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if containers := ReadSandboxContainers(context.Background(), binary); len(containers) != 0 {
		t.Fatalf("expected no containers, got %+v", containers)
	}
}
