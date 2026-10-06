package app

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBattleBoundedSimulationAndServerScoring(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	a := b.arena("house", "room", "host")
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.add(membership{ID: "host", DisplayName: "Player"})
	p.Connected = true
	p.InputX = 999
	p.LastInput = time.Now()
	a.phase = "play"
	a.ends = time.Now().Add(time.Minute)
	a.coins = []battleCoin{{ID: "spark", X: p.X, Y: p.Y}}
	a.tick(time.Now(), 1.0/30)
	if p.Score != 1 {
		t.Fatalf("server failed to score contact: %d", p.Score)
	}
	for i := 0; i < 1000; i++ {
		p.LastInput = time.Now()
		a.tick(time.Now(), 1.0/30)
	}
	if p.X > 1000-a.radius() || p.X < a.radius() {
		t.Fatalf("escaped arena: %f", p.X)
	}
}
func TestBattleRoundsPersistMutationsAndReset(t *testing.T) {
	b := newBattleManager()
	b.apiKey = ""
	defer b.close()
	a := b.arena("house", "room", "host")
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.build(now)
	a.submissions["host"] = battleSubmission{Prompt: "ice and lava", Matched: interpretBattle("ice and lava")}
	b.resolve(a, now)
	if a.phase != "play" || !a.has("ice") || !a.has("lava") {
		t.Fatal("mutations not applied")
	}
	a.tick(now.Add(46*time.Second), 1.0/30)
	if a.phase != "results" {
		t.Fatal("round did not end")
	}
	a.tick(now.Add(56*time.Second), 1.0/30)
	if a.round != 2 || !a.has("ice") {
		t.Fatal("mutations did not persist")
	}
	a.reset()
	if a.round != 0 || len(a.mutations) != 0 || a.phase != "lobby" {
		t.Fatal("reset failed")
	}
}
func TestBattlePlanRejectsUnknownAndDuplicateMutations(t *testing.T) {
	for _, plan := range []battlePlan{{Mutations: []string{"execute-code"}, Summary: "bad"}, {Mutations: []string{"ice", "ice"}, Summary: "bad"}, {Mutations: []string{}, Summary: strings.Repeat("x", 241)}, {}} {
		if validateBattlePlan(plan) == nil {
			t.Fatal("invalid provider plan accepted")
		}
	}
	if validateBattlePlan(battlePlan{Mutations: []string{"ice", "lava"}, Summary: "Slippery lava arena."}) != nil {
		t.Fatal("valid plan rejected")
	}
}

type battleTestTransport func(*http.Request) (*http.Response, error)

func (f battleTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBattleProviderStructuredResponse(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	b.apiKey = "test-key"
	b.client = &http.Client{Transport: battleTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Error("missing server-side provider key")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["generationConfig"] == nil || request["systemInstruction"] == nil {
			t.Error("missing structured output constraints")
		}
		envelope := map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": `{"mutations":["ice","wind"],"summary":"A slippery, windy arena."}`}}}}}}
		raw, _ := json.Marshal(envelope)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})}
	plan, err := b.generate(context.Background(), []string{"turn the arena into a frozen storm"})
	if err != nil || len(plan.Mutations) != 2 {
		t.Fatalf("provider parsing failed: %#v %v", plan, err)
	}
}
func TestBattleProviderFailureFallsBack(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	b.apiKey = "test-key"
	b.client = &http.Client{Transport: battleTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
	})}
	a := b.arena("house", "room", "host")
	a.mu.Lock()
	a.build(time.Now())
	a.submissions["host"] = battleSubmission{Prompt: "ice", Matched: []string{"ice"}}
	b.resolve(a, time.Now())
	a.mu.Unlock()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("fallback never completed")
		case <-ticker.C:
			a.mu.Lock()
			done := a.phase == "play"
			if done && (a.mode != "local-fallback" || !a.has("ice")) {
				t.Error("provider failure was not labeled or fallback not applied")
			}
			a.mu.Unlock()
			if done {
				return
			}
		}
	}
}
func TestBattleResetInvalidatesPendingGeneration(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	a := b.arena("house", "room", "host")
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.epoch
	a.phase = "resolve"
	a.reset()
	if a.epoch == old || a.phase != "lobby" {
		t.Fatal("reset did not invalidate pending generation")
	}
}
func TestBattleInputExpires(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	a := b.arena("h", "r", "p")
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.add(membership{ID: "p"})
	p.Connected = true
	p.InputX = 1
	p.LastInput = time.Now().Add(-time.Second)
	a.phase = "play"
	a.ends = time.Now().Add(time.Minute)
	a.tick(time.Now(), 1.0/30)
	if p.InputX != 0 {
		t.Fatal("stale movement survived disconnect timeout")
	}
}
func TestBattleHTTPAccessAndHostPermissions(t *testing.T) {
	application, err := New(Config{DatabasePath: filepath.Join(t.TempDir(), "test.db"), StaticDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	ctx := context.Background()
	host, hostToken, _ := application.store.createSession(ctx)
	house, _, _ := application.store.createHouse(ctx, host.ID, "Arena", "Host")
	guest, guestToken, _ := application.store.createSession(ctx)
	guestID := randomID("mem")
	_, err = application.store.db.Exec(`INSERT INTO house_memberships(id,house_id,session_id,display_name,last_room_id,joined_at) VALUES(?,?,?,?,?,?)`, guestID, house.ID, guest.ID, "Guest", house.Rooms[0].ID, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	query := "?houseId=" + house.ID + "&roomId=" + house.Rooms[0].ID
	request := func(action, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/battle/"+action+query, strings.NewReader(body))
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "roomcade_guest", Value: token})
		}
		res := httptest.NewRecorder()
		application.Handler().ServeHTTP(res, req)
		return res
	}
	if r := request("join", "", "{}"); r.Code != 401 {
		t.Fatalf("anonymous join: %d", r.Code)
	}
	outsider, outsiderToken, _ := application.store.createSession(ctx)
	_ = outsider
	if r := request("join", outsiderToken, "{}"); r.Code != 403 {
		t.Fatalf("outsider joined: %d", r.Code)
	}
	if r := request("join", hostToken, "{}"); r.Code != 200 {
		t.Fatalf("host join: %d %s", r.Code, r.Body.String())
	}
	if r := request("start", guestToken, "{}"); r.Code != 403 {
		t.Fatalf("guest started: %d", r.Code)
	}
	if r := request("start", hostToken, "{}"); r.Code != 200 {
		t.Fatalf("host start: %d", r.Code)
	}
	if r := request("prompt", guestToken, `{"prompt":"icy floor"}`); r.Code != 200 {
		t.Fatalf("guest prompt: %d", r.Code)
	}
	if r := request("advance", guestToken, "{}"); r.Code != 403 {
		t.Fatalf("guest advanced: %d", r.Code)
	}
	if r := request("advance", hostToken, "{}"); r.Code != 200 {
		t.Fatalf("host advance: %d", r.Code)
	}
	r := request("input", guestToken, `{"x":1000,"y":-1000}`)
	if r.Code != 200 {
		t.Fatalf("input: %d", r.Code)
	}
	arena := application.battles.arena(house.ID, house.Rooms[0].ID, house.SelfMemberID)
	arena.mu.Lock()
	p := arena.players[guestID]
	if p.InputX != 1 || p.InputY != -1 {
		t.Error("client input not clamped")
	}
	arena.mu.Unlock()
	var response map[string]any
	if err = json.Unmarshal(r.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if r := request("reset", guestToken, "{}"); r.Code != 403 {
		t.Fatalf("guest reset: %d", r.Code)
	}
	if r := request("reset", hostToken, "{}"); r.Code != 200 {
		t.Fatalf("host reset: %d", r.Code)
	}
	if r := request("demo", guestToken, "{}"); r.Code != 403 {
		t.Fatalf("guest ran host demo: %d", r.Code)
	}
	if r := request("demo", hostToken, "{}"); r.Code != 200 {
		t.Fatalf("host demo failed: %d", r.Code)
	}

}

func TestBattleTwoClientsReceiveSameAuthoritativeWorld(t *testing.T) {
	application, err := New(Config{DatabasePath: filepath.Join(t.TempDir(), "test.db"), StaticDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	ctx := context.Background()
	host, hostToken, _ := application.store.createSession(ctx)
	house, _, _ := application.store.createHouse(ctx, host.ID, "Sync demo", "Host")
	guest, guestToken, _ := application.store.createSession(ctx)
	guestID := randomID("mem")
	_, err = application.store.db.Exec(`INSERT INTO house_memberships(id,house_id,session_id,display_name,last_room_id,joined_at) VALUES(?,?,?,?,?,?)`, guestID, house.ID, guest.ID, "Guest", house.Rooms[0].ID, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.Handler())
	defer server.Close()
	query := "?houseId=" + house.ID + "&roomId=" + house.Rooms[0].ID
	open := func(token string) *http.Response {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/battle/events"+query, nil)
		request.AddCookie(&http.Cookie{Name: "roomcade_guest", Value: token})
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 {
			t.Fatalf("stream rejected: %d", response.StatusCode)
		}
		return response
	}
	first := open(hostToken)
	defer first.Body.Close()
	second := open(guestToken)
	defer second.Body.Close()
	arena := application.battles.arena(house.ID, house.Rooms[0].ID, house.SelfMemberID)
	arena.mu.Lock()
	arena.phase = "play"
	arena.ends = time.Now().Add(time.Minute)
	arena.mutations = []string{"ice", "lava"}
	program, compileErr := compileBattleProgram(testBattleProgram())
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	arena.programs = []*compiledBattleProgram{program}
	arena.coins = []battleCoin{{ID: "shared-spark", X: 900, Y: 600}}
	arena.players[guestID].InputX = 1
	arena.players[guestID].LastInput = time.Now()
	arena.mu.Unlock()
	read := func(response *http.Response) battleSnapshot {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var snap battleSnapshot
				if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &snap); err != nil {
					t.Fatal(err)
				}
				if snap.Phase == "play" && len(snap.Players) == 2 {
					return snap
				}
			}
		}
		t.Fatal("stream ended before shared world")
		return battleSnapshot{}
	}
	left, right := read(first), read(second)
	if left.Code != right.Code || left.Round != right.Round || strings.Join(left.Mutations, ",") != strings.Join(right.Mutations, ",") || len(left.Hazards) != len(right.Hazards) {
		t.Fatal("clients received different worlds")
	}
	if len(left.Programs) != 1 || len(right.Programs) != 1 || len(left.Features) != 1 || len(right.Features) != 1 || left.Programs[0] != right.Programs[0] {
		t.Fatal("clients did not receive the same generated feature code")
	}
	if left.Coins[0].ID != "shared-spark" || right.Coins[0].ID != "shared-spark" {
		t.Fatal("coin world differed")
	}
	arena.mu.Lock()
	if arena.players[guestID].X <= 200 {
		t.Error("authoritative simulation did not move second player")
	}
	arena.mu.Unlock()
}
