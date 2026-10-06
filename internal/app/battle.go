package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The arena is ephemeral. Membership remains in SQLite; simulation never trusts
// client coordinates or scores. AI programs use a bounded expression interpreter.
type battlePlayer struct {
	requestBuckets map[string]*battleRequestBucket
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	X              float64   `json:"x"`
	Y              float64   `json:"y"`
	VX             float64   `json:"vx"`
	VY             float64   `json:"vy"`
	Score          int       `json:"score"`
	RoundScore     int       `json:"roundScore"`
	Color          int       `json:"color"`
	Connected      bool      `json:"connected"`
	Cooldown       float64   `json:"cooldown"`
	InputX         float64   `json:"-"`
	InputY         float64   `json:"-"`
	LastInput      time.Time `json:"-"`
	Connections    int       `json:"-"`
}
type battleCoin struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}
type battleHazard struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	R    float64 `json:"r"`
	Type string  `json:"type"`
}
type battleSubmission struct {
	Name    string   `json:"name"`
	Prompt  string   `json:"prompt"`
	Matched []string `json:"matched"`
}
type battlePlan struct {
	Programs  []battleProgram `json:"programs"`
	Mutations []string        `json:"mutations"`
	Summary   string          `json:"summary"`
}
type battleSnapshot struct {
	Features    []battleFeature    `json:"features"`
	Programs    []battleProgram    `json:"programs"`
	Code        string             `json:"code"`
	Host        string             `json:"host"`
	Phase       string             `json:"phase"`
	PhaseEndsAt int64              `json:"phaseEndsAt"`
	ServerNow   int64              `json:"serverNow"`
	Round       int                `json:"round"`
	Mutations   []string           `json:"mutations"`
	Events      []battleSubmission `json:"events"`
	Submitted   []string           `json:"submitted"`
	Radius      float64            `json:"radius"`
	Players     []battlePlayer     `json:"players"`
	Coins       []battleCoin       `json:"coins"`
	Hazards     []battleHazard     `json:"hazards"`
	Mode        string             `json:"mode"`
	Summary     string             `json:"summary"`
}
type battleArena struct {
	programs                     []*compiledBattleProgram
	mu                           sync.Mutex
	houseID, roomID, host, phase string
	round                        int
	ends                         time.Time
	elapsed                      float64
	players                      map[string]*battlePlayer
	submissions                  map[string]battleSubmission
	mutations                    []string
	events                       []battleSubmission
	coins                        []battleCoin
	mode, summary                string
	epoch                        uint64
	lastActive                   time.Time
}
type battleManager struct {
	budget        *battleAIBudget
	mu            sync.Mutex
	arenas        map[string]*battleArena
	done          chan struct{}
	once          sync.Once
	apiKey, model string
	client        *http.Client
}

var battleRules = []struct {
	id, name string
	pattern  *regexp.Regexp
}{
	{"ice", "Ice floor", regexp.MustCompile(`ice|icy|slip|skate|friction`)},
	{"speed", "Overclocked", regexp.MustCompile(`fast|speed|turbo|quick`)},
	{"giant", "Big energy", regexp.MustCompile(`big|giant|huge|large`)},
	{"tiny", "Pocket players", regexp.MustCompile(`tiny|small|shrink|mini`)},
	{"wind", "Crosswind", regexp.MustCompile(`wind|storm|gravity|moon|float`)},
	{"lava", "Lava bloom", regexp.MustCompile(`lava|fire|burn|hot|flood`)},
	{"reverse", "Wrong way", regexp.MustCompile(`reverse|invert|backward|opposite`)},
	{"chaos", "Pinball hazards", regexp.MustCompile(`chaos|ball|bounce|hazard|meteor|break`)},
}

func interpretBattle(prompt string) []string {
	out := []string{}
	for _, rule := range battleRules {
		if rule.pattern.MatchString(strings.ToLower(prompt)) {
			out = append(out, rule.id)
			if len(out) == 2 {
				break
			}
		}
	}
	return out
}
func validateBattlePlan(plan battlePlan) error {
	if plan.Mutations == nil || strings.TrimSpace(plan.Summary) == "" {
		return errors.New("mutation plan is missing required fields")
	}
	if len(plan.Mutations) > 8 || len(plan.Summary) > 240 {
		return errors.New("mutation plan exceeds limits")
	}
	seen := map[string]bool{}
	for _, id := range plan.Mutations {
		valid := false
		for _, r := range battleRules {
			if r.id == id {
				valid = true
			}
		}
		if !valid || seen[id] {
			return errors.New("invalid mutation plan")
		}
		seen[id] = true
	}
	if len(plan.Programs) > 2 {
		return errors.New("too many generated programs")
	}
	for _, program := range plan.Programs {
		if _, err := compileBattleProgram(program); err != nil {
			return err
		}
	}
	return nil
}
func newBattleManager(stores ...*Store) *battleManager {
	model := os.Getenv("BATTLE_AI_MODEL")
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	b := &battleManager{budget: newBattleAIBudget(stores...), arenas: map[string]*battleArena{}, done: make(chan struct{}), apiKey: os.Getenv("GEMINI_API_KEY"), model: model, client: &http.Client{Timeout: 15 * time.Second}}
	go b.run()
	return b
}
func (b *battleManager) close() { b.once.Do(func() { close(b.done) }) }
func (b *battleManager) arena(house, room, host string) *battleArena {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := house + ":" + room
	a := b.arenas[key]
	if a == nil {
		a = &battleArena{houseID: house, roomID: room, host: host, phase: "lobby", players: map[string]*battlePlayer{}, submissions: map[string]battleSubmission{}, mutations: []string{}, events: []battleSubmission{}, coins: []battleCoin{}, mode: "local-rules", lastActive: time.Now()}
		b.arenas[key] = a
	}
	return a
}
func (b *battleManager) run() {
	t := time.NewTicker(time.Second / 30)
	defer t.Stop()
	for {
		select {
		case <-b.done:
			return
		case now := <-t.C:
			b.mu.Lock()
			for key, a := range b.arenas {
				a.mu.Lock()
				if now.Sub(a.lastActive) > 10*time.Minute {
					a.epoch++
					delete(b.arenas, key)
					a.mu.Unlock()
					continue
				}
				a.tick(now, 1.0/30)
				if a.phase == "build" && !now.Before(a.ends) {
					b.resolve(a, now)
				}
				a.mu.Unlock()
			}
			b.mu.Unlock()
		}
	}
}
func (a *battleArena) add(m membership) *battlePlayer {
	p := a.players[m.ID]
	if p == nil {
		p = &battlePlayer{ID: m.ID, Name: m.DisplayName, X: 100 + float64(len(a.players))*100, Y: 320, Color: len(a.players) % 8}
		a.players[m.ID] = p
	}
	p.Name = m.DisplayName
	a.lastActive = time.Now()
	return p
}
func (a *battleArena) build(now time.Time) {
	a.round++
	a.phase = "build"
	a.ends = now.Add(25 * time.Second)
	a.submissions = map[string]battleSubmission{}
	for _, p := range a.players {
		p.RoundScore = 0
		p.InputX = 0
		p.InputY = 0
	}
}
func (a *battleArena) play(now time.Time, plan battlePlan) {
	a.events = []battleSubmission{}
	ids := make([]string, 0, len(a.submissions))
	for id := range a.submissions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a.events = append(a.events, a.submissions[id])
	}
	for _, id := range plan.Mutations {
		found := false
		for _, old := range a.mutations {
			if id == old {
				found = true
			}
		}
		if !found {
			a.mutations = append(a.mutations, id)
		}
	}
	for _, spec := range plan.Programs {
		if len(a.programs) >= 6 {
			break
		}
		program, err := compileBattleProgram(spec)
		if err == nil {
			a.programs = append(a.programs, program)
		}
	}
	for _, program := range a.programs {
		program.last = map[string]float64{}
	}
	a.summary = plan.Summary
	a.phase = "play"
	a.ends = now.Add(45 * time.Second)
	a.elapsed = 0
	i := 0
	for _, p := range a.players {
		p.X = 110 + float64(i%4)*240
		p.Y = 170 + float64(i/4)*300
		p.VX = 0
		p.VY = 0
		p.InputX = 0
		p.InputY = 0
		p.Cooldown = 1
		i++
	}
	a.coins = make([]battleCoin, 16)
	for i := range a.coins {
		a.coins[i] = newBattleCoin()
	}
}
func (a *battleArena) reset() {
	a.epoch++
	a.phase = "lobby"
	a.round = 0
	a.ends = time.Time{}
	a.mutations = []string{}
	a.programs = nil
	a.events = []battleSubmission{}
	a.coins = []battleCoin{}
	a.submissions = map[string]battleSubmission{}
	a.summary = ""
	a.mode = "local-rules"
	for _, p := range a.players {
		p.Score = 0
		p.RoundScore = 0
		p.InputX = 0
		p.InputY = 0
		p.VX = 0
		p.VY = 0
	}
	a.lastActive = time.Now()
}
func newBattleCoin() battleCoin {
	return battleCoin{ID: randomID("spark"), X: 50 + rand.Float64()*900, Y: 50 + rand.Float64()*540}
}
func (a *battleArena) has(id string) bool {
	for _, m := range a.mutations {
		if m == id {
			return true
		}
	}
	return false
}
func (a *battleArena) radius() float64 {
	if a.has("giant") {
		return 29
	}
	if a.has("tiny") {
		return 9
	}
	return 17
}
func (a *battleArena) hazards() []battleHazard {
	out := []battleHazard{}
	if a.has("lava") {
		r := math.Min(85, 35+a.elapsed*.9)
		out = append(out, battleHazard{300, 230, r, "lava"}, battleHazard{700, 430, r, "lava"})
	}
	if a.has("chaos") {
		for i := 0; i < 4; i++ {
			out = append(out, battleHazard{500 + math.Sin(a.elapsed*.8+float64(i)*1.8)*410, 320 + math.Cos(a.elapsed*.65+float64(i)*2.2)*240, 22, "chaos"})
		}
	}
	return out
}
func clampBattle(n, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, n)) }
func (a *battleArena) tick(now time.Time, dt float64) {
	if a.phase == "play" && !now.Before(a.ends) {
		a.phase = "results"
		a.ends = now.Add(9 * time.Second)
	} else if a.phase == "results" && !now.Before(a.ends) {
		if a.round >= 3 {
			a.phase = "finished"
			a.ends = time.Time{}
		} else {
			a.build(now)
		}
	}
	if a.phase != "play" {
		return
	}
	a.elapsed += dt
	r := a.radius()
	damping, accel := 14.0, 2800.0
	if a.has("ice") {
		damping = 1.8
		accel = 750
	}
	speed, reverse := 1.0, 1.0
	if a.has("speed") {
		speed = 1.45
	}
	if a.has("reverse") {
		reverse = -1
	}
	hazards := a.hazards()
	for _, p := range a.players {
		if !p.Connected {
			p.InputX = 0
			p.InputY = 0
			p.VX = 0
			p.VY = 0
			continue
		}
		p.Cooldown = math.Max(0, p.Cooldown-dt)
		if now.Sub(p.LastInput) > 400*time.Millisecond {
			p.InputX = 0
			p.InputY = 0
		}
		length := math.Max(1, math.Hypot(p.InputX, p.InputY))
		p.VX = p.VX*math.Exp(-damping*dt) + p.InputX/length*reverse*accel*speed*dt
		p.VY = p.VY*math.Exp(-damping*dt) + p.InputY/length*reverse*accel*speed*dt
		if a.has("wind") {
			p.VX += math.Sin(a.elapsed*.6) * 250 * dt
			p.VY += math.Cos(a.elapsed*.8) * 170 * dt
		}
		a.applyPrograms(p, dt)
		p.X = clampBattle(p.X+p.VX*dt, r, 1000-r)
		p.Y = clampBattle(p.Y+p.VY*dt, r, 640-r)
		if p.X <= r || p.X >= 1000-r {
			p.VX *= -.5
		}
		if p.Y <= r || p.Y >= 640-r {
			p.VY *= -.5
		}
		for i, c := range a.coins {
			if math.Hypot(p.X-c.X, p.Y-c.Y) < r+11 {
				p.Score++
				p.RoundScore++
				a.coins[i] = newBattleCoin()
			}
		}
		if p.Cooldown == 0 {
			for _, h := range hazards {
				if math.Hypot(p.X-h.X, p.Y-h.Y) < r+h.R {
					p.Score = max(0, p.Score-2)
					p.RoundScore = max(0, p.RoundScore-2)
					p.X = 500
					p.Y = 320
					p.VX = 0
					p.VY = 0
					p.Cooldown = 1.5
					break
				}
			}
		}
	}
}
func (a *battleArena) snapshot(now time.Time) battleSnapshot {
	players := []battlePlayer{}
	for _, p := range a.players {
		players = append(players, *p)
	}
	sort.Slice(players, func(i, j int) bool { return players[i].ID < players[j].ID })
	submitted := []string{}
	for id := range a.submissions {
		submitted = append(submitted, id)
	}
	ends := int64(0)
	if !a.ends.IsZero() {
		ends = a.ends.UnixMilli()
	}
	programs := []battleProgram{}
	for _, program := range a.programs {
		programs = append(programs, program.spec)
	}
	return battleSnapshot{Features: a.features(), Programs: programs, Code: a.roomID, Host: a.host, Phase: a.phase, PhaseEndsAt: ends, ServerNow: now.UnixMilli(), Round: a.round, Mutations: a.mutations, Events: a.events, Submitted: submitted, Radius: a.radius(), Players: players, Coins: a.coins, Hazards: a.hazards(), Mode: a.mode, Summary: a.summary}
}
func localBattlePlan(submissions map[string]battleSubmission) battlePlan {
	plan := battlePlan{Mutations: []string{}, Summary: "Local rules applied supported keywords. No AI model was called."}
	seen := map[string]bool{}
	for _, s := range submissions {
		for _, id := range s.Matched {
			if !seen[id] {
				seen[id] = true
				plan.Mutations = append(plan.Mutations, id)
			}
		}
	}
	sort.Strings(plan.Mutations)
	return plan
}

// Caller holds the arena lock. Resolve off the simulation and HTTP command paths.
func (b *battleManager) resolve(a *battleArena, now time.Time) {
	if a.phase != "build" {
		return
	}
	fallback := localBattlePlan(a.submissions)
	if b.apiKey == "" || len(a.submissions) == 0 {
		a.mode = "local-rules"
		a.play(now, fallback)
		return
	}
	a.phase = "resolve"
	a.ends = now.Add(16 * time.Second)
	a.epoch++
	epoch := a.epoch
	prompts := []string{}
	for _, s := range a.submissions {
		prompts = append(prompts, s.Prompt)
	}
	sort.Strings(prompts)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		plan, err := b.generate(ctx, prompts)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.epoch != epoch || a.phase != "resolve" {
			return
		}
		if err != nil {
			plan = fallback
			plan.Summary = "AI unavailable or invalid output. Local rules used for this round."
			a.mode = "local-fallback"
			var limit *battleAILimitError
			if errors.As(err, &limit) {
				a.mode = "local-limited"
				plan.Summary = limit.Error() + " Local keyword rules used; no model was called."
			}
		} else {
			a.mode = "gemini"
		}
		a.play(time.Now(), plan)
	}()
}
func (b *battleManager) generate(ctx context.Context, prompts []string) (battlePlan, error) {
	if len(prompts) > maxHouseMembers {
		return battlePlan{}, errors.New("too many prompts")
	}
	for _, prompt := range prompts {
		if len(prompt) > 180 {
			return battlePlan{}, errors.New("prompt exceeds limit")
		}
	}

	ids := []string{}
	description := []string{}
	for _, r := range battleRules {
		ids = append(ids, r.id)
		description = append(description, r.id+": "+r.name)
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"mutations": map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "enum": ids}}, "programs": battleProgramSchema(), "summary": map[string]any{"type": "string", "maxLength": 240}}, "required": []string{"mutations", "programs", "summary"}}
	rawPrompts, _ := json.Marshal(prompts)
	payload := map[string]any{"systemInstruction": map[string]any{"parts": []map[string]string{{"text": battleProgramInstructions + strings.Join(description, "; ")}}}, "contents": []map[string]any{{"parts": []map[string]string{{"text": string(rawPrompts)}}}}, "generationConfig": map[string]any{"responseFormat": map[string]any{"text": map[string]any{"mimeType": "application/json", "schema": schema}}, "maxOutputTokens": 2048, "temperature": 0.7}}
	body, _ := json.Marshal(payload)
	if len(body) > 16000 {
		return battlePlan{}, errors.New("AI request exceeds size limit")
	}
	release, err := b.budget.reserve(ctx, time.Now())
	if err != nil {
		return battlePlan{}, err
	}
	defer release()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models/"+url.PathEscape(b.model)+":generateContent", bytes.NewReader(body))
	if err != nil {
		return battlePlan{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-goog-api-key", b.apiKey)
	response, err := b.client.Do(request)
	if err != nil {
		return battlePlan{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return battlePlan{}, fmt.Errorf("provider status %d", response.StatusCode)
	}
	var envelope struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&envelope); err != nil {
		return battlePlan{}, err
	}
	if len(envelope.Candidates) == 0 || len(envelope.Candidates[0].Content.Parts) == 0 {
		return battlePlan{}, errors.New("provider returned no plan")
	}
	var plan battlePlan
	decoder := json.NewDecoder(strings.NewReader(envelope.Candidates[0].Content.Parts[0].Text))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return plan, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return plan, errors.New("trailing plan data")
	}
	return plan, validateBattlePlan(plan)
}

func (a *App) battleAccess(r *http.Request, ss session) (membership, string, error) {
	house, room := r.URL.Query().Get("houseId"), r.URL.Query().Get("roomId")
	m, err := a.store.membership(r.Context(), house, ss.ID)
	if err != nil {
		return m, "", err
	}
	if m.LastRoomID != room {
		return m, "", errForbidden
	}
	var host string
	err = a.store.db.QueryRowContext(r.Context(), `SELECT h.host_member_id FROM houses h JOIN rooms r ON r.house_id=h.id WHERE h.id=? AND r.id=?`, house, room).Scan(&host)
	return m, host, err
}
func (a *App) handleBattleJoin(w http.ResponseWriter, r *http.Request, ss session) {
	m, host, err := a.battleAccess(r, ss)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	arena := a.battles.arena(m.HouseID, m.LastRoomID, host)
	arena.mu.Lock()
	defer arena.mu.Unlock()
	arena.host = host
	arena.add(m)
	writeData(w, http.StatusOK, map[string]any{"id": m.ID, "code": m.LastRoomID, "aiConfigured": a.battles.apiKey != "", "model": a.battles.model, "capacity": maxHouseMembers, "aiDailyLimit": a.battles.budget.caps[2], "aiMonthlyLimit": a.battles.budget.caps[3]})
}
func (a *App) handleBattleCommand(w http.ResponseWriter, r *http.Request, ss session) {
	m, host, err := a.battleAccess(r, ss)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	arena := a.battles.arena(m.HouseID, m.LastRoomID, host)
	var data struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Prompt string  `json:"prompt"`
	}
	if err = decodeJSON(r, &data); err != nil {
		handleStoreError(w, r, err)
		return
	}
	arena.mu.Lock()
	defer arena.mu.Unlock()
	arena.host = host
	p := arena.add(m)
	action := r.PathValue("action")
	now := time.Now()
	kind := "control"
	if action == "input" || action == "prompt" {
		kind = action
	}
	if !p.allowRequest(kind, now) {
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests. Wait a moment.", nil)
		return
	}
	if action == "input" {
		if math.IsNaN(data.X) || math.IsInf(data.X, 0) || math.IsNaN(data.Y) || math.IsInf(data.Y, 0) {
			handleStoreError(w, r, errors.New("invalid input"))
			return
		}
		p.InputX = clampBattle(data.X, -1, 1)
		p.InputY = clampBattle(data.Y, -1, 1)
		p.LastInput = now
		writeData(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if action == "prompt" {
		if arena.phase != "build" {
			handleStoreError(w, r, errors.New("prompts open only during mutation rounds"))
			return
		}
		prompt := strings.TrimSpace(data.Prompt)
		if len(prompt) == 0 || len(prompt) > 180 {
			handleStoreError(w, r, errors.New("prompt must be 1–180 bytes"))
			return
		}
		matched := interpretBattle(prompt)
		arena.submissions[m.ID] = battleSubmission{Name: m.DisplayName, Prompt: prompt, Matched: matched}
		writeData(w, http.StatusOK, map[string]any{"matched": matched, "supported": len(matched) > 0, "aiConfigured": a.battles.apiKey != ""})
		return
	}
	if m.ID != host {
		handleStoreError(w, r, errForbidden)
		return
	}
	switch action {
	case "start":
		if arena.phase != "lobby" {
			handleStoreError(w, r, errors.New("session already started"))
			return
		}
		arena.build(now)
	case "advance":
		if arena.phase == "build" {
			a.battles.resolve(arena, now)
		} else if arena.phase == "results" {
			if arena.round < 3 {
				arena.build(now)
			} else {
				arena.phase = "finished"
				arena.ends = time.Time{}
			}
		} else {
			handleStoreError(w, r, errors.New("nothing to advance"))
			return
		}
	case "demo":
		if arena.phase != "lobby" {
			handleStoreError(w, r, errors.New("reset before trying the demo"))
			return
		}
		arena.build(now)
		arena.mode = "script-demo"
		arena.play(now, battlePlan{Mutations: []string{}, Programs: []battleProgram{{Name: "Orbit portal", X: "500+250*cos(t)", Y: "320+180*sin(t)", Radius: "35", ForceX: "0", ForceY: "0", Points: "2", TeleportX: "1000-px", TeleportY: "640-py", Cooldown: 3}}, Summary: "Handwritten ArenaScript example. No AI model called."})
	case "reset":
		arena.reset()
	default:
		handleStoreError(w, r, errors.New("unknown battle command"))
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"ok": true})
}
func (a *App) handleBattleEvents(w http.ResponseWriter, r *http.Request, ss session) {
	m, host, err := a.battleAccess(r, ss)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, 500, "stream_unavailable", "Streaming unavailable", nil)
		return
	}
	arena := a.battles.arena(m.HouseID, m.LastRoomID, host)
	arena.mu.Lock()
	p := arena.add(m)
	if p.Connections >= 2 {
		arena.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "This player already has two arena connections.", nil)
		return
	}
	p.Connections++
	p.Connected = true
	arena.mu.Unlock()
	defer func() {
		arena.mu.Lock()
		p.Connections--
		p.Connected = p.Connections > 0
		p.InputX = 0
		p.InputY = 0
		arena.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	ticker := time.NewTicker(time.Second / 20)
	defer ticker.Stop()
	lastCheck := time.Time{}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.battles.done:
			return
		case now := <-ticker.C:
			if now.Sub(lastCheck) > time.Second {
				_, host, err = a.battleAccess(r, ss)
				if err != nil {
					return
				}
				lastCheck = now
			}
			arena.mu.Lock()
			arena.host = host
			arena.lastActive = now
			// Reconcile revoked or moved members so stale actors cannot keep collecting.
			if now.Equal(lastCheck) {
				for id, player := range arena.players {
					var count int
					_ = a.store.db.QueryRowContext(r.Context(), `SELECT count(*) FROM house_memberships WHERE id=? AND house_id=? AND last_room_id=?`, id, m.HouseID, m.LastRoomID).Scan(&count)
					if count == 0 {
						player.InputX = 0
						player.InputY = 0
						delete(arena.players, id)
						delete(arena.submissions, id)
					}
				}
			}
			raw, marshalErr := json.Marshal(arena.snapshot(now))
			arena.mu.Unlock()
			if marshalErr != nil {
				return
			}
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, err = fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
