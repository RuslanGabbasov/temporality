package storage

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLoadAvg(t *testing.T) {
	load1, load5 := parseLoadAvg("3.24 2.83 1.10 2/1521 5501\n")
	require.InDelta(t, 3.24, load1, 1e-9)
	require.InDelta(t, 2.83, load5, 1e-9)

	load1, load5 = parseLoadAvg("garbage")
	require.InDelta(t, 0, load1, 1e-9)
	require.InDelta(t, 0, load5, 1e-9)
}

func TestParseMemInfo(t *testing.T) {
	raw := "MemTotal:       16253564 kB\nMemFree:         1234567 kB\nMemAvailable:    8123456 kB\n"
	total, available := parseMemInfo(raw)
	require.Equal(t, uint64(16253564*1024), total)
	require.Equal(t, uint64(8123456*1024), available)
}

func TestParseDockerDF(t *testing.T) {
	// Modern docker emits numeric byte sizes.
	numeric := "{\"Type\":\"Images\",\"TotalCount\":10,\"Size\":6894283123}\n" +
		"{\"Type\":\"Containers\",\"TotalCount\":4,\"Size\":0}\n" +
		"{\"Type\":\"Local Volumes\",\"TotalCount\":3,\"Size\":0}\n"
	bytes, err := parseDockerDF([]byte(numeric))
	require.NoError(t, err)
	require.Equal(t, int64(6894283123), bytes)

	// Older docker emits humanized strings, sometimes with percentages.
	human := "{\"Type\":\"Images\",\"Size\":\"2.4GB\"}\n"
	bytes, err = parseDockerDF([]byte(human))
	require.NoError(t, err)
	require.Equal(t, int64(2.4e9), bytes)

	humanReclaim := "{\"Type\":\"Images\",\"Size\":\"1.5GiB (30%)\"}\n"
	bytes, err = parseDockerDF([]byte(humanReclaim))
	require.NoError(t, err)
	require.Equal(t, int64(1.5*float64(1<<30)), bytes)

	kib := "{\"Type\":\"Images\",\"Size\":\"512KiB\"}\n"
	bytes, err = parseDockerDF([]byte(kib))
	require.NoError(t, err)
	require.Equal(t, int64(512*(1<<10)), bytes)

	// No images row at all.
	_, err = parseDockerDF([]byte("{\"Type\":\"Containers\",\"Size\":0}\n"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestParseDockerSizeQuoted(t *testing.T) {
	// json.RawMessage keeps the quotes; parseDockerSize must unquote strings.
	raw := json.RawMessage(`"2.4GB"`)
	require.Equal(t, int64(2.4e9), parseDockerSize(raw))
	require.Equal(t, int64(42), parseDockerSize(json.RawMessage(`42`)))
	require.Equal(t, int64(0), parseDockerSize(json.RawMessage(`null`)))
	require.Equal(t, int64(0), parseDockerSize(json.RawMessage(`""`)))
}
