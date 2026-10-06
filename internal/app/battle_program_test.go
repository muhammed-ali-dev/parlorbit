package app

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testBattleProgram() battleProgram {
	return battleProgram{Name: "Orbit portal", X: "500+250*cos(t)", Y: "320+180*sin(t)", Radius: "35", ForceX: "0", ForceY: "0", Points: "2", TeleportX: "1000-px", TeleportY: "640-py", Cooldown: 3}
}
func TestBattleProgramRejectsHostCodeAndUnboundedExpressions(t *testing.T) {
	for _, expr := range []string{"os.Exit(0)", "fetch(1)", "px", "1<<30", "func(){}()", "sin(1,2)", "10001", "true", "x[0]", strings.Repeat("1+", 90) + "1", strings.Repeat("(", 15) + "1" + strings.Repeat(")", 15)} {
		p := testBattleProgram()
		p.X = expr
		if _, err := compileBattleProgram(p); err == nil {
			t.Errorf("accepted %q", expr)
		}
	}
	p := testBattleProgram()
	p.Cooldown = 0
	if _, err := compileBattleProgram(p); err == nil {
		t.Fatal("accepted zero cooldown")
	}
	p = testBattleProgram()
	p.TeleportY = ""
	if _, err := compileBattleProgram(p); err == nil {
		t.Fatal("accepted partial portal")
	}
}
func TestBattleProgramSharedPortalAndCooldown(t *testing.T) {
	program, err := compileBattleProgram(testBattleProgram())
	if err != nil {
		t.Fatal(err)
	}
	a := &battleArena{programs: []*compiledBattleProgram{program}}
	first := &battlePlayer{ID: "one", X: 750, Y: 320}
	second := &battlePlayer{ID: "two", X: 750, Y: 320}
	a.applyPrograms(first, 1.0/30)
	a.applyPrograms(second, 1.0/30)
	for _, p := range []*battlePlayer{first, second} {
		if p.X != 250 || p.Y != 320 || p.Score != 2 {
			t.Fatalf("portal not shared: %+v", p)
		}
		p.X = 750
		a.applyPrograms(p, 1.0/30)
		if p.Score != 2 || p.X != 750 {
			t.Fatal("cooldown did not stop repeat trigger")
		}
	}
	a.elapsed = math.Pi / 2
	features := a.features()
	if math.Abs(features[0].X-500) > 0.001 || math.Abs(features[0].Y-500) > 0.001 {
		t.Fatal("AI formula not executed")
	}
	a.reset()
	if len(a.programs) != 0 {
		t.Fatal("reset retained code")
	}
}
func TestBattleProgramInvalidArithmeticDoesNotCorruptWorld(t *testing.T) {
	spec := testBattleProgram()
	spec.ForceX = "1/(t-t)"
	program, err := compileBattleProgram(spec)
	if err != nil {
		t.Fatal(err)
	}
	a := &battleArena{programs: []*compiledBattleProgram{program}}
	p := &battlePlayer{ID: "p", X: 750, Y: 320}
	a.applyPrograms(p, 1.0/30)
	if math.IsNaN(p.X) || p.Score != 0 || p.X != 750 {
		t.Fatal("invalid expression altered world")
	}
}
func TestBattleProviderGeneratesAndAppliesNewCode(t *testing.T) {
	b := newBattleManager()
	defer b.close()
	b.apiKey = "test-key"
	b.client = &http.Client{Transport: battleTestTransport(func(r *http.Request) (*http.Response, error) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		config := request["generationConfig"].(map[string]any)
		if config["maxOutputTokens"].(float64) != 2048 || config["responseFormat"] == nil {
			t.Error("missing cost or schema bounds")
		}
		plan := battlePlan{Mutations: []string{}, Programs: []battleProgram{testBattleProgram()}, Summary: "An orbiting portal awards two sparks."}
		body, _ := json.Marshal(plan)
		envelope, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": string(body)}}}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(envelope))), Header: make(http.Header)}, nil
	})}
	plan, err := b.generate(context.Background(), []string{"add a moving portal"})
	if err != nil {
		t.Fatal(err)
	}
	a := b.arena("h", "r", "p")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.play(time.Now(), plan)
	if len(a.programs) != 1 || len(a.snapshot(time.Now()).Features) != 1 {
		t.Fatal("generated code wasn't hot-applied")
	}
	a.build(time.Now())
	if len(a.programs) != 1 {
		t.Fatal("code did not persist into next round")
	}
}
