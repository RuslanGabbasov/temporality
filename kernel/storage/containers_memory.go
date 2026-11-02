package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Host-wide container memory breakdown (the dashboard's «Куда ушла память»):
// the Server widget shows the HOST memory line, while the Sandboxes widget
// only lists temporality sandbox containers — everything else on the host
// (postgres, temporal, the kernel itself, the journal, OS daemons) was
// invisible, so a 20 GiB «used» next to a 244 MB sandbox looked like a leak.
// One-shot docker stats over ALL running containers, top consumers first.

// ContainerMemory is one running container's memory sample.
type ContainerMemory struct {
	Name    string  `json:"name"`
	Image   string  `json:"image,omitempty"`
	UsedMB  int     `json:"used_mb"`
	LimitMB int     `json:"limit_mb,omitempty"`
	Percent float64 `json:"percent"`
}

// ReadContainerMemory samples memory for every running container (one-shot
// docker stats, a ~1s window). Bounded timeout; on any failure it degrades
// to an empty list and the widget hides.
func ReadContainerMemory(ctx context.Context, binary string) []ContainerMemory {
	if binary == "" {
		binary = "docker"
	}
	callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	out, err := exec.CommandContext(callCtx, binary, "stats", "--no-stream", "--format", "{{json .}}").Output()
	if err != nil {
		return nil
	}
	containers := make([]ContainerMemory, 0, 16)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Name     string `json:"Name"`
			Image    string `json:"Image"`
			MemUsage string `json:"MemUsage"`
			MemPerc  string `json:"MemPerc"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil || row.Name == "" {
			continue
		}
		container := ContainerMemory{Name: row.Name, Image: row.Image}
		if used, limit, ok := parseDockerUsage(row.MemUsage); ok {
			container.UsedMB = int(used / (1 << 20))
			container.LimitMB = int(limit / (1 << 20))
		}
		container.Percent = parseDockerPercent(row.MemPerc)
		containers = append(containers, container)
	}
	// Top consumers first; docker stats rows have no stable order.
	sort.Slice(containers, func(i, j int) bool { return containers[i].UsedMB > containers[j].UsedMB })
	return containers
}

// FormatBytesMB renders a MB value docker-style (GiB above 1024) for the
// widget rows.
func FormatBytesMB(mb int) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GiB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}
