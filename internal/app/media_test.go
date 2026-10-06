package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMediaTokenRestrictsPublishingAndRoomMembership(t *testing.T) {
	store := testStore(t)
	a := &App{store: store, cfg: Config{LiveKitURL: "ws://localhost:7880", LiveKitAPIKey: "testkey", LiveKitSecret: "testsecret"}, hubs: newHubManager(store)}
	ss, _, err := store.createSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	house, _, err := store.createHouse(context.Background(), ss.ID, "Media House", "Alex")
	if err != nil {
		t.Fatal(err)
	}
	member, err := store.membership(context.Background(), house.ID, ss.ID)
	if err != nil {
		t.Fatal(err)
	}
	hub := a.hubs.get(house.ID)
	issue := func(roomID string, caller session) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/voice-token", nil)
		request.SetPathValue("houseId", house.ID)
		request.SetPathValue("roomId", roomID)
		response := httptest.NewRecorder()
		a.handleVoiceToken(response, request, caller)
		return response
	}
	if response := issue(house.Rooms[0].ID, ss); response.Code != http.StatusForbidden {
		t.Fatalf("offline member got media token: %d", response.Code)
	}
	hub.clients[ss.ID] = &realtimeClient{sessionID: ss.ID, memberID: member.ID}
	if response := issue("another-room", ss); response.Code != http.StatusForbidden {
		t.Fatalf("wrong room got media token: %d", response.Code)
	}
	stranger, _, err := store.createSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response := issue(house.Rooms[0].ID, stranger); response.Code != http.StatusForbidden {
		t.Fatalf("nonmember got media token: %d", response.Code)
	}
	response := issue(house.Rooms[0].ID, ss)
	if response.Code != http.StatusOK {
		t.Fatalf("active member denied: %d %s", response.Code, response.Body.String())
	}
	var payload struct{ Data struct{ Token string } }
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(payload.Data.Token, ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Name  string
		Video struct {
			Room              string
			RoomJoin          bool
			CanPublish        bool
			CanSubscribe      bool
			CanPublishSources []string
		}
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Name != "Alex" || claims.Video.Room != "roomcade_"+house.Rooms[0].ID || !claims.Video.RoomJoin || !claims.Video.CanPublish || !claims.Video.CanSubscribe {
		t.Fatalf("incorrect participant grants: %+v", claims)
	}
	if strings.Join(claims.Video.CanPublishSources, ",") != "microphone,camera" {
		t.Fatalf("unexpected sources: %v", claims.Video.CanPublishSources)
	}
}

func TestCameraPolicyAllowsOnlyApplicationOrigin(t *testing.T) {
	a := &App{}
	response := httptest.NewRecorder()
	a.securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	policy := response.Header().Get("Permissions-Policy")
	if policy != "camera=(self), geolocation=(), microphone=(self)" {
		t.Fatalf("unexpected device policy: %s", policy)
	}
}

func TestLiveKitCustomEndpointAllowedByCSP(t *testing.T) {
	a := &App{cfg: Config{LiveKitURL: "wss://calls.example.com:8443"}}
	response := httptest.NewRecorder()
	a.securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, " https://calls.example.com:8443;") {
		t.Fatalf("custom LiveKit endpoint blocked: %s", policy)
	}
}
