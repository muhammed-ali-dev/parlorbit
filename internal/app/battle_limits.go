package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type battleAILimitError struct{ reason string }

func (e *battleAILimitError) Error() string { return e.reason }

type battleAIBudget struct {
	mu       sync.Mutex
	db       *sql.DB
	enabled  bool
	caps     [4]int
	counts   map[string]int
	inFlight bool
}

func battleLimitEnv(name string, fallback int) int {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
func newBattleAIBudget(stores ...*Store) *battleAIBudget {
	enabled := true
	if value, exists := os.LookupEnv("BATTLE_AI_ENABLED"); exists {
		enabled = strings.EqualFold(value, "true")
	}
	budget := &battleAIBudget{enabled: enabled, caps: [4]int{battleLimitEnv("BATTLE_AI_MAX_MINUTE_CALLS", 2), battleLimitEnv("BATTLE_AI_MAX_HOURLY_CALLS", 10), battleLimitEnv("BATTLE_AI_MAX_DAILY_CALLS", 30), battleLimitEnv("BATTLE_AI_MAX_MONTHLY_CALLS", 300)}, counts: map[string]int{}}
	if len(stores) > 0 {
		budget.db = stores[0].db
	}
	return budget
}

// Reserve BEFORE the provider request. Failed/abandoned attempts aren't refunded.
// Fixed UTC buckets are persisted across arenas, House deletion, and restarts.
func (b *battleAIBudget) reserve(ctx context.Context, now time.Time) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	deny := func(reason string) (func(), error) { return nil, &battleAILimitError{reason} }
	if !b.enabled {
		return deny("AI is disabled by the server owner.")
	}
	if b.inFlight {
		return deny("Another AI request is running.")
	}
	now = now.UTC()
	buckets := [4]string{"minute:" + now.Format("2006-01-02T15:04"), "hour:" + now.Format("2006-01-02T15"), "day:" + now.Format("2006-01-02"), "month:" + now.Format("2006-01")}
	labels := [4]string{"minute", "hour", "day", "month"}
	var tx *sql.Tx
	if b.db != nil {
		var err error
		tx, err = b.db.BeginTx(ctx, nil)
		if err != nil {
			return deny("AI usage counter is unavailable.")
		}
		defer tx.Rollback()
	}
	for i, key := range buckets {
		count := b.counts[key]
		if tx != nil {
			err := tx.QueryRowContext(ctx, "SELECT attempts FROM battle_ai_usage WHERE bucket=?", key).Scan(&count)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return deny("AI usage counter is unavailable.")
			}
		}
		if count >= b.caps[i] {
			return deny("AI limit reached for this " + labels[i] + ".")
		}
	}
	if tx != nil {
		for _, key := range buckets {
			if _, err := tx.ExecContext(ctx, "INSERT INTO battle_ai_usage(bucket,attempts,created_at) VALUES(?,1,?) ON CONFLICT(bucket) DO UPDATE SET attempts=attempts+1", key, now.Unix()); err != nil {
				return deny("AI usage counter is unavailable.")
			}
		}
		// Old buckets no longer authorize anything; bound storage growth.
		if _, err := tx.ExecContext(ctx, "DELETE FROM battle_ai_usage WHERE created_at<?", now.AddDate(0, -2, 0).Unix()); err != nil {
			return deny("AI usage counter is unavailable.")
		}
		if err := tx.Commit(); err != nil {
			return deny("AI usage counter is unavailable.")
		}
	} else {
		for _, key := range buckets {
			b.counts[key]++
		}
	}
	b.inFlight = true
	return func() { b.mu.Lock(); b.inFlight = false; b.mu.Unlock() }, nil
}

type battleRequestBucket struct {
	tokens  float64
	updated time.Time
}

func (p *battlePlayer) allowRequest(kind string, now time.Time) bool {
	if p.requestBuckets == nil {
		p.requestBuckets = map[string]*battleRequestBucket{}
	}
	rate, burst := 1.0, 4.0
	if kind == "input" {
		rate = 40
		burst = 40
	}
	if kind == "prompt" {
		rate = 1
		burst = 3
	}
	bucket := p.requestBuckets[kind]
	if bucket == nil {
		bucket = &battleRequestBucket{tokens: burst, updated: now}
		p.requestBuckets[kind] = bucket
	}
	bucket.tokens = clampBattle(bucket.tokens+now.Sub(bucket.updated).Seconds()*rate, 0, burst)
	bucket.updated = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}
