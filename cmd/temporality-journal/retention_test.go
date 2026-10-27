package main

import (
	"testing"
	"time"
)

func envWith(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

var (
	wantInterval = defaultRetentionInterval
	wantAge      = defaultRetentionAge
)

// The env contract: "off" disables, everything unparsable (including "0" and
// negatives) falls back to the defaults, valid durations win. A typo must
// never silently change the schedule, and "0" must never become a
// wipe-everything retention age.
func TestRetentionConfigFromEnv(t *testing.T) {
	cases := []struct {
		name   string
		getenv func(string) string
		want   retentionConfig
	}{
		{"unset: defaults, enabled", envWith(nil),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"empty strings: defaults, enabled", envWith(map[string]string{retentionIntervalEnv: "", retentionAgeEnv: ""}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"valid overrides", envWith(map[string]string{retentionIntervalEnv: "15m", retentionAgeEnv: "24h"}),
			retentionConfig{Interval: 15 * time.Minute, Age: 24 * time.Hour, Enabled: true}},
		{"interval off disables", envWith(map[string]string{retentionIntervalEnv: "off"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: false}},
		{"age off disables", envWith(map[string]string{retentionAgeEnv: "off"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: false}},
		{"both off stays disabled", envWith(map[string]string{retentionIntervalEnv: "off", retentionAgeEnv: "off"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: false}},
		{"interval 0 falls back to default", envWith(map[string]string{retentionIntervalEnv: "0"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"interval invalid falls back to default", envWith(map[string]string{retentionIntervalEnv: "every 6 hours"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"interval negative falls back to default", envWith(map[string]string{retentionIntervalEnv: "-5m"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"age 0 falls back to default (never wipe-all)", envWith(map[string]string{retentionAgeEnv: "0"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"age invalid falls back to default", envWith(map[string]string{retentionAgeEnv: "ninety days"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"age negative falls back to default", envWith(map[string]string{retentionAgeEnv: "-1h"}),
			retentionConfig{Interval: wantInterval, Age: wantAge, Enabled: true}},
		{"invalid interval keeps valid age", envWith(map[string]string{retentionIntervalEnv: "oops", retentionAgeEnv: "72h"}),
			retentionConfig{Interval: wantInterval, Age: 72 * time.Hour, Enabled: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := retentionConfigFromEnv(tc.getenv)
			if got != tc.want {
				t.Fatalf("retentionConfigFromEnv = %+v, want %+v", got, tc.want)
			}
		})
	}
}
