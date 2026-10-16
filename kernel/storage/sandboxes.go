package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SandboxContainer is one running sandbox container with its live resource
// usage and the run-identity labels stamped at creation (kernel/sandbox
// Request.Labels). It backs the dashboard's sandbox bubbles: the integral
// load and the hover digest (who initiated, which agent, which run).
type SandboxContainer struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Labels       map[string]string `json:"labels,omitempty"`
	CPUPercent   float64           `json:"cpu_percent"`
	MemUsedBytes int64             `json:"mem_used_bytes"`
	MemLimit     int64             `json:"mem_limit_bytes"`
	MemPercent   float64           `json:"mem_percent"`
	NetInBytes   int64             `json:"net_in_bytes"`
	NetOutBytes  int64             `json:"net_out_bytes"`
}

// ContainerNamePrefix marks Temporality sandboxes; the health endpoint only
// reports these, never unrelated host containers.
const ContainerNamePrefix = "temporality-sandbox-"

// ReadSandboxContainers lists running Temporality sandbox containers with a
// one-shot docker stats sample (a ~1s window, so CPU is a real reading, not a
// lifetime average). Bounded timeout: health rendering must never hang on an
// unresponsive daemon — on any failure it degrades to an empty list and the
// dashboard widget just hides.
func ReadSandboxContainers(ctx context.Context, binary string) []SandboxContainer {
	if binary == "" {
		binary = "docker"
	}
	callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	rows, err := sandboxPsRows(callCtx, binary)
	if err != nil || len(rows) == 0 {
		return nil
	}
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Strings(names)

	stats, err := sandboxStatsRows(callCtx, binary, names)
	if err != nil {
		return nil
	}
	containers := make([]SandboxContainer, 0, len(stats))
	for _, s := range stats {
		row := rows[s.Name]
		s.Image = row.image
		if len(row.labels) > 0 {
			s.Labels = row.labels
		}
		containers = append(containers, s)
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers
}

type sandboxPsRow struct {
	image  string
	labels map[string]string
}

// sandboxPsRows lists running sandbox containers with their labels. docker ps
// renders labels as a flat "k=v,k=v" list; our label keys and values never
// contain commas.
func sandboxPsRows(ctx context.Context, binary string) (map[string]sandboxPsRow, error) {
	out, err := exec.CommandContext(ctx, binary, "ps", "--filter", "name="+ContainerNamePrefix, "--format", "{{json .}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}
	rows := map[string]sandboxPsRow{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Names  string `json:"Names"`
			Labels string `json:"Labels"`
			Image  string `json:"Image"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		name, image, labels := parseSandboxPsLine(row.Names, row.Labels, row.Image)
		if !strings.HasPrefix(name, ContainerNamePrefix) {
			continue
		}
		parsed := sandboxPsRow{image: image, labels: labels}
		if len(parsed.labels) == 0 {
			parsed.labels = nil
		}
		rows[name] = parsed
	}
	return rows, nil
}

// parseSandboxPsLine splits docker ps's flat "k=v,k=v" label list into a map.
func parseSandboxPsLine(name, rawLabels, image string) (string, string, map[string]string) {
	labels := map[string]string{}
	for _, pair := range strings.Split(rawLabels, ",") {
		if key, value, found := strings.Cut(strings.TrimSpace(pair), "="); found && key != "" {
			labels[key] = value
		}
	}
	return name, image, labels
}

// sandboxStatsRows samples one-shot stats for the given containers. docker
// stats accepts container names positionally.
func sandboxStatsRows(ctx context.Context, binary string, names []string) (map[string]SandboxContainer, error) {
	args := append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, names...)
	out, err := exec.CommandContext(ctx, binary, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("stats sandbox containers: %w", err)
	}
	stats := map[string]SandboxContainer{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Name     string `json:"Name"`
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
			MemPerc  string `json:"MemPerc"`
			NetIO    string `json:"NetIO"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil || row.Name == "" {
			continue
		}
		container := SandboxContainer{
			Name:       row.Name,
			CPUPercent: parseDockerPercent(row.CPUPerc),
			MemPercent: parseDockerPercent(row.MemPerc),
		}
		if used, limit, ok := parseDockerUsage(row.MemUsage); ok {
			container.MemUsedBytes = used
			container.MemLimit = limit
		}
		if in, out, ok := parseDockerNetIO(row.NetIO); ok {
			container.NetInBytes = in
			container.NetOutBytes = out
		}
		stats[row.Name] = container
	}
	return stats, nil
}

// parseDockerPercent reads "12.34%" (or "0.00%").
func parseDockerPercent(raw string) float64 {
	value, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(raw), "%"), 64)
	return value
}

// parseDockerUsage reads docker's "123.4MiB / 1.5GiB" pair.
func parseDockerUsage(raw string) (int64, int64, bool) {
	used, rest, ok := strings.Cut(strings.TrimSpace(raw), " / ")
	if !ok {
		return 0, 0, false
	}
	limit, err := parseDockerBytes(rest)
	if err != nil {
		return 0, 0, false
	}
	usedBytes, err := parseDockerBytes(used)
	if err != nil {
		return 0, 0, false
	}
	return usedBytes, limit, true
}

// parseDockerNetIO reads docker's "1.2kB / 45kB" (input / output cumulative).
func parseDockerNetIO(raw string) (int64, int64, bool) {
	in, rest, ok := strings.Cut(strings.TrimSpace(raw), " / ")
	if !ok {
		return 0, 0, false
	}
	outBytes, err := parseDockerBytes(rest)
	if err != nil {
		return 0, 0, false
	}
	inBytes, err := parseDockerBytes(in)
	if err != nil {
		return 0, 0, false
	}
	return inBytes, outBytes, true
}

func parseDockerBytes(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0B" {
		return 0, nil
	}
	// Split the numeric part from the unit suffix.
	i := 0
	for i < len(raw) && (raw[i] >= '0' && raw[i] <= '9' || raw[i] == '.' || raw[i] == '-') {
		i++
	}
	value, err := strconv.ParseFloat(raw[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("parse docker byte value %q: %w", raw, err)
	}
	multiplier, ok := dockerByteUnit(strings.TrimSpace(raw[i:]))
	if !ok {
		return 0, fmt.Errorf("unknown docker byte unit in %q", raw)
	}
	return int64(value * multiplier), nil
}

func dockerByteUnit(unit string) (float64, bool) {
	switch unit {
	case "B", "":
		return 1, true
	case "kB":
		return 1e3, true
	case "KB", "KiB":
		return 1 << 10, true
	case "MB", "MiB":
		return 1 << 20, true
	case "GB", "GiB":
		return 1 << 30, true
	case "TB", "TiB":
		return 1 << 40, true
	default:
		return 0, false
	}
}
