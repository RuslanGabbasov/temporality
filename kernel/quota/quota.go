// Package quota bounds daily agent runs per project. The limit is the cost
// guardrail a team shares one model budget under: one runaway loop must not
// burn the day's quota for everyone. Quotas count accepted run starts per
// UTC day; rejected starts never consume quota.
package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Unlimited means no cap is configured for the project.
const Unlimited = -1

type Limiter struct {
	pool    *pgxpool.Pool
	def     int
	perProj map[string]int
}

// Open connects its own pool to the kernel database and parses the quota
// config. An empty config yields a disabled limiter, never an error.
func Open(ctx context.Context, databaseURL, config string) (*Limiter, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	limiter, err := FromConfig(pool, config)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return limiter, nil
}

func (l *Limiter) Close() {
	if l != nil && l.pool != nil {
		l.pool.Close()
	}
}

// Migrate applies the quota table DDL from a migration file, mirroring the
// outbox pattern: the kernel binary owns its producer-side state.
func (l *Limiter) Migrate(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = l.pool.Exec(ctx, string(data))
	return err
}

// FromConfig parses KERNEL_RUN_QUOTAS, a comma list of "project:limit"
// entries where "*" sets the default. Entries without "*" leave the default
// unlimited. Invalid entries are rejected loudly: a mistyped limit must fail
// startup, not silently disable the guardrail.
func FromConfig(pool *pgxpool.Pool, config string) (*Limiter, error) {
	limiter := &Limiter{pool: pool, def: Unlimited, perProj: map[string]int{}}
	for _, entry := range strings.Split(config, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, limit, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, fmt.Errorf("quota entry %q must be project:limit", entry)
		}
		name = strings.TrimSpace(name)
		value, err := strconv.Atoi(strings.TrimSpace(limit))
		if err != nil || value < 0 {
			return nil, fmt.Errorf("quota limit for %q must be a non-negative integer", name)
		}
		if name == "*" {
			limiter.def = value
			continue
		}
		limiter.perProj[name] = value
	}
	return limiter, nil
}

// Limit returns the configured daily limit for the project (Unlimited when
// uncapped) and whether any quota concept applies at all.
func (l *Limiter) Limit(project string) int {
	if l == nil {
		return Unlimited
	}
	if limit, ok := l.perProj[project]; ok {
		return limit
	}
	return l.def
}

func (l *Limiter) Enabled() bool {
	return l != nil && (l.def != Unlimited || len(l.perProj) > 0)
}

// Default returns the fallback limit for projects without an entry.
func (l *Limiter) Default() int {
	if l == nil {
		return Unlimited
	}
	return l.def
}

// Verdict is the outcome of one quota check.
type Verdict struct {
	Allowed bool `json:"allowed"`
	Limit   int  `json:"limit"`
	Used    int  `json:"used"`
	// ResetAt is when the daily window rolls over (UTC midnight).
	ResetAt time.Time `json:"reset_at"`
}

// Allow atomically consumes one run slot for the project today. The
// conditional upsert makes the check-and-increment one statement: concurrent
// starts cannot overshoot the limit, and a rejected start increments nothing.
func (l *Limiter) Allow(ctx context.Context, project string) (Verdict, error) {
	limit := l.Limit(project)
	now := time.Now().UTC()
	verdict := Verdict{Limit: limit, ResetAt: resetAt(now)}
	if limit == Unlimited {
		return Verdict{Allowed: true, Limit: limit, ResetAt: verdict.ResetAt}, nil
	}
	if limit == 0 {
		return Verdict{Limit: 0, ResetAt: verdict.ResetAt}, nil
	}
	var used int
	err := l.pool.QueryRow(ctx, `INSERT INTO kernel_run_quota_day(project, day, runs)
		VALUES ($1, $2, 1)
		ON CONFLICT (project, day) DO UPDATE SET runs = kernel_run_quota_day.runs + 1
		WHERE kernel_run_quota_day.runs < $3
		RETURNING runs`, project, now.Format("2006-01-02"), limit).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) {
		// Over the limit: report the real usage for the operator.
		usage, usageErr := l.Usage(ctx, project)
		if usageErr == nil {
			return Verdict{Limit: limit, Used: usage.Used, ResetAt: verdict.ResetAt}, nil
		}
		return Verdict{Limit: limit, ResetAt: verdict.ResetAt}, nil
	}
	if err != nil {
		return Verdict{Limit: limit, ResetAt: verdict.ResetAt}, fmt.Errorf("quota check: %w", err)
	}
	return Verdict{Allowed: true, Limit: limit, Used: used, ResetAt: verdict.ResetAt}, nil
}

// Usage reports the current daily usage without consuming anything.
func (l *Limiter) Usage(ctx context.Context, project string) (Verdict, error) {
	limit := l.Limit(project)
	now := time.Now().UTC()
	verdict := Verdict{Limit: limit, ResetAt: resetAt(now)}
	if limit == Unlimited {
		return verdict, nil
	}
	err := l.pool.QueryRow(ctx, `SELECT runs FROM kernel_run_quota_day WHERE project=$1 AND day=$2`, project, now.Format("2006-01-02")).Scan(&verdict.Used)
	if errors.Is(err, pgx.ErrNoRows) {
		return verdict, nil
	}
	if err != nil {
		return Verdict{Limit: limit, ResetAt: verdict.ResetAt}, fmt.Errorf("quota usage: %w", err)
	}
	return verdict, nil
}

func resetAt(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}
