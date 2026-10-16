package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Host health probes (docs/sandbox-storage.md §5.4): statfs per path, load
// average and memory from /proc, plus the docker footprint through the same
// binary the kernel uses for sandboxes. Everything is best-effort: a missing
// /proc (macOS dev machines) yields zeros and an unreachable docker daemon
// yields Available=false — the dashboard shows dashes, not errors.

// DiskPath is one filesystem probe.
type DiskPath struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
}

// CPUStats is load average against the visible core count.
type CPUStats struct {
	Load1 float64 `json:"load1"`
	Load5 float64 `json:"load5"`
	Cores int     `json:"cores"`
}

// MemStats is host memory from /proc/meminfo.
type MemStats struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

// HostHealth bundles the probes the dashboard's server widget renders.
type HostHealth struct {
	Disk   []DiskPath `json:"disk"`
	CPU    CPUStats   `json:"cpu"`
	Memory MemStats   `json:"memory"`
}

// DockerFootprint is the docker store footprint; Available=false means the
// binary or daemon was unreachable and the numbers must not be trusted.
type DockerFootprint struct {
	ImagesBytes       int64 `json:"images_bytes"`
	ContainersRunning int   `json:"containers_running"`
	Available         bool  `json:"available"`
}

// ReadHostHealth probes the given paths plus CPU and memory. Duplicate and
// failing paths are skipped silently — the widget degrades, not breaks.
func ReadHostHealth(paths ...string) HostHealth {
	var health HostHealth
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		usage, err := diskUsage(p)
		if err != nil {
			continue
		}
		seen[p] = true
		health.Disk = append(health.Disk, DiskPath{Path: p, TotalBytes: usage.TotalBytes, FreeBytes: usage.FreeBytes})
	}
	health.CPU = CPUStats{Cores: runtime.NumCPU()}
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		health.CPU.Load1, health.CPU.Load5 = parseLoadAvg(string(raw))
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		health.Memory.TotalBytes, health.Memory.AvailableBytes = parseMemInfo(string(raw))
	}
	return health
}

// diskUsage stats one filesystem path. In the kernel's standard Docker
// deployment this reflects the host filesystem, which is exactly the health
// signal the dashboard needs.
func diskUsage(path string) (DiskUsage, error) {
	total, free, err := statfsBytes(path)
	if err != nil {
		return DiskUsage{}, err
	}
	return DiskUsage{TotalBytes: total, FreeBytes: free}, nil
}

// ReadDockerFootprint asks the docker CLI for the image store size and the
// running container count. Short timeout: health rendering must never hang on
// an unresponsive daemon.
func ReadDockerFootprint(ctx context.Context, binary string) DockerFootprint {
	if binary == "" {
		binary = "docker"
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	df, dfErr := runDockerDF(callCtx, binary)
	ps, psErr := runDockerPS(callCtx, binary)
	if dfErr != nil || psErr != nil {
		return DockerFootprint{Available: false}
	}
	return DockerFootprint{ImagesBytes: df, ContainersRunning: ps, Available: true}
}

func runDockerDF(ctx context.Context, binary string) (int64, error) {
	out, err := exec.CommandContext(ctx, binary, "system", "df", "--format", "{{json .}}").Output()
	if err != nil {
		return 0, err
	}
	return parseDockerDF(out)
}

func runDockerPS(ctx context.Context, binary string) (int, error) {
	out, err := exec.CommandContext(ctx, binary, "ps", "-q").Output()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
}

// parseDockerDF extracts the images row size from `docker system df --format
// '{{json .}}'` output (one JSON object per line). Docker versions disagree
// whether Size is numeric bytes or a humanized string, so both are accepted.
func parseDockerDF(out []byte) (int64, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row struct {
			Type string          `json:"Type"`
			Size json.RawMessage `json:"Size"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Type != "Images" {
			continue
		}
		return parseDockerSize(row.Size), nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, os.ErrNotExist
}

// parseDockerSize accepts 6894283123, "2.4GB" and "1.2kB (30%)" alike.
func parseDockerSize(raw json.RawMessage) int64 {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0
	}
	// Numeric form (raw bytes).
	if n, err := strconv.ParseFloat(text, 64); err == nil {
		return int64(n)
	}
	// String form: strip the reclaimable percentage, then the unit suffix.
	if s, err := strconv.Unquote(text); err == nil {
		text = s
	}
	if idx := strings.IndexByte(text, '('); idx > 0 {
		text = text[:idx]
	}
	text = strings.TrimSpace(text)
	upper := strings.ToUpper(text)
	multiplier := float64(1)
	// Multi-character units first: the bare "B" arm must come last or it
	// would eat the tail of every "GB"/"KiB" before they can match.
	for _, unit := range []struct {
		suffix string
		value  float64
	}{
		{"TIB", 1 << 40}, {"TB", 1e12},
		{"GIB", 1 << 30}, {"GB", 1e9},
		{"MIB", 1 << 20}, {"MB", 1e6},
		{"KIB", 1 << 10}, {"KB", 1e3},
		{"B", 1},
	} {
		if strings.HasSuffix(upper, unit.suffix) {
			multiplier = unit.value
			text = strings.TrimSpace(text[:len(text)-len(unit.suffix)])
			break
		}
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0
	}
	return int64(n * multiplier)
}

// parseLoadAvg reads "3.24 2.83 1.10 2/1521 5501".
func parseLoadAvg(raw string) (load1, load5 float64) {
	fields := strings.Fields(raw)
	if len(fields) >= 1 {
		load1, _ = strconv.ParseFloat(fields[0], 64)
	}
	if len(fields) >= 2 {
		load5, _ = strconv.ParseFloat(fields[1], 64)
	}
	return load1, load5
}

// parseMemInfo reads MemTotal and MemAvailable lines (kB values).
func parseMemInfo(raw string) (total, available uint64) {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = memInfoKb(line) * 1024
		case strings.HasPrefix(line, "MemAvailable:"):
			available = memInfoKb(line) * 1024
		}
	}
	return total, available
}

func memInfoKb(line string) uint64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	n, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
