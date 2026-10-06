package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBattleBudgetSurvivesRestartAndMonthlyLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	b := newBattleAIBudget(store)
	b.enabled = true
	b.caps = [4]int{100, 100, 2, 2}
	for i := 0; i < 2; i++ {
		release, err := b.reserve(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	store.Close()
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	b = newBattleAIBudget(reopened)
	b.enabled = true
	b.caps = [4]int{100, 100, 2, 2}
	if _, err := b.reserve(context.Background(), now); err == nil {
		t.Fatal("restart reset daily allowance")
	}
	if _, err := b.reserve(context.Background(), now.Add(24*time.Hour)); err == nil || !strings.Contains(err.Error(), "month") {
		t.Fatal("next day bypassed monthly cap", err)
	}
	release, err := b.reserve(context.Background(), now.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal("new month did not reset", err)
	}
	release()
}
func TestBattleBudgetAllWindowsAndKillSwitch(t *testing.T) {
	for index, label := range []string{"minute", "hour", "day", "month"} {
		t.Run(label, func(t *testing.T) {
			b := newBattleAIBudget()
			b.enabled = true
			b.caps = [4]int{100, 100, 100, 100}
			b.caps[index] = 1
			now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			release, err := b.reserve(context.Background(), now)
			if err != nil {
				t.Fatal(err)
			}
			release()
			if _, err := b.reserve(context.Background(), now); err == nil || !strings.Contains(err.Error(), label) {
				t.Fatal("window not capped", err)
			}
		})
	}
	b := newBattleAIBudget()
	b.enabled = false
	if _, err := b.reserve(context.Background(), time.Now()); err == nil {
		t.Fatal("kill switch did not block")
	}
	b.enabled = true
	b.caps = [4]int{0, 100, 100, 100}
	if _, err := b.reserve(context.Background(), time.Now()); err == nil {
		t.Fatal("zero cap did not block")
	}
}
func TestBattleBudgetConcurrentReservationsCannotOverspend(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := newBattleAIBudget(store)
			b.enabled = true
			b.caps = [4]int{100, 100, 1, 100}
			release, err := b.reserve(context.Background(), now)
			if err == nil {
				successes.Add(1)
				release()
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("expected one billed attempt, got %d", successes.Load())
	}
}
func TestBattleBudgetDatabaseFailureBlocksProvider(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatal(err)
	}
	budget := newBattleAIBudget(store)
	budget.enabled = true
	store.Close()
	if _, err := budget.reserve(context.Background(), time.Now()); err == nil {
		t.Fatal("counter failure allowed spending")
	}
}
func TestBattleProviderFailuresCountAndLimitStopsNetwork(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	b.apiKey = "test"
	b.budget.enabled = true
	b.budget.caps = [4]int{100, 100, 1, 100}
	calls := 0
	b.client = &http.Client{Transport: battleTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("failed")), Header: make(http.Header)}, nil
	})}
	if _, err := b.generate(context.Background(), []string{"ice"}); err == nil {
		t.Fatal("expected provider failure")
	}
	_, err := b.generate(context.Background(), []string{"ice"})
	var limit *battleAILimitError
	if !errors.As(err, &limit) || calls != 1 {
		t.Fatal("failed call refunded or limit hit provider", err, calls)
	}
}
func TestBattleBudgetOnlyOneInFlight(t *testing.T) {
	b := newBattleAIBudget()
	b.enabled = true
	release, err := b.reserve(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.reserve(context.Background(), time.Now()); err == nil {
		t.Fatal("parallel AI request allowed")
	}
	release()
	release, err = b.reserve(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestBattlePlayerRequestLimitsAllowGameplayAndThrottleSpam(t *testing.T) {
	p := &battlePlayer{}
	now := time.Now()
	for i := 0; i < 100; i++ {
		if !p.allowRequest("input", now.Add(time.Duration(i)*50*time.Millisecond)) {
			t.Fatal("normal movement throttled")
		}
	}
	for i := 0; i < 3; i++ {
		if !p.allowRequest("prompt", now) {
			t.Fatal("initial prompt burst throttled")
		}
	}
	if p.allowRequest("prompt", now) {
		t.Fatal("prompt spam allowed")
	}
	if !p.allowRequest("prompt", now.Add(time.Second)) {
		t.Fatal("prompt allowance did not refill")
	}
	for i := 0; i < 4; i++ {
		if !p.allowRequest("control", now) {
			t.Fatal("reset/start burst denied")
		}
	}
	if p.allowRequest("control", now) {
		t.Fatal("reset spam allowed")
	}
}

func TestBattleRateLimitedRoundFallsBackWithoutProviderCall(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	b.apiKey = "test"
	b.budget.enabled = true
	b.budget.caps = [4]int{0, 100, 100, 100}
	var calls atomic.Int32
	b.client = &http.Client{Transport: battleTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not call provider")
	})}
	a := b.arena("h", "r", "host")
	a.mu.Lock()
	a.build(time.Now())
	a.submissions["host"] = battleSubmission{Prompt: "ice", Matched: []string{"ice"}}
	b.resolve(a, time.Now())
	a.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		done := a.phase == "play"
		mode := a.mode
		ice := a.has("ice")
		a.mu.Unlock()
		if done {
			if mode != "local-limited" || !ice || calls.Load() != 0 {
				t.Fatal("budget fallback wasn't safe", mode, calls.Load())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("budget blocked the game instead of falling back")
}
