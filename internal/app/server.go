package app

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/webhook"
)

type App struct {
	cfg      Config
	store    *Store
	hubs     *hubManager
	mux      *http.ServeMux
	commands sync.Mutex
	battles  *battleManager
}

func New(cfg Config) (*App, error) {
	if err := validateMediaConfig(cfg); err != nil {
		return nil, err
	}
	if err := validateDemoConfig(cfg); err != nil {
		return nil, err
	}
	store, err := OpenStore(cfg.DatabasePath)
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, store: store, hubs: newHubManager(store), mux: http.NewServeMux()}
	a.battles = newBattleManager(store)
	a.hubs.commands = &a.commands
	a.hubs.revokeVoice = a.revokeVoice
	a.routes()
	return a, nil
}

func (a *App) Close() error { a.battles.close(); return a.store.Close() }

func (a *App) Handler() http.Handler {
	return a.securityHeaders(a.requestContext(a.demoGate(a.mux)))
}

func (a *App) routes() {
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	a.mux.HandleFunc("GET /readyz", a.handleReady)
	a.mux.HandleFunc("GET /metrics", a.handleMetrics)
	a.mux.HandleFunc("POST /api/v1/guest-session", a.handleGuestSession)
	a.mux.HandleFunc("GET /api/v1/bootstrap", a.withSession(a.handleBootstrap))
	a.mux.HandleFunc("POST /api/v1/houses", a.withSession(a.handleCreateHouse))
	a.mux.HandleFunc("GET /api/v1/houses/{houseId}", a.withSession(a.handleGetHouse))
	a.mux.HandleFunc("PATCH /api/v1/houses/{houseId}", a.withSession(a.handleUpdateHouse))
	a.mux.HandleFunc("DELETE /api/v1/houses/{houseId}", a.withSession(a.handleDeleteHouse))
	a.mux.HandleFunc("POST /api/v1/houses/{houseId}/host-transfer", a.withSession(a.handleHostTransfer))
	a.mux.HandleFunc("POST /api/v1/houses/{houseId}/rooms", a.withSession(a.handleCreateRoom))
	a.mux.HandleFunc("PATCH /api/v1/houses/{houseId}/rooms/{roomId}", a.withSession(a.handleUpdateRoom))
	a.mux.HandleFunc("DELETE /api/v1/houses/{houseId}/rooms/{roomId}", a.withSession(a.handleDeleteRoom))
	a.mux.HandleFunc("GET /api/v1/invites/{token}", a.handleInvitePreview)
	a.mux.HandleFunc("POST /api/v1/invites/{token}/requests", a.withSession(a.handleJoinRequest))
	a.mux.HandleFunc("GET /api/v1/join-requests/{requestId}", a.withSession(a.handleJoinStatus))
	a.mux.HandleFunc("POST /api/v1/houses/{houseId}/join-requests/{requestId}/decision", a.withSession(a.handleJoinDecision))
	a.mux.HandleFunc("POST /api/v1/houses/{houseId}/invite/rotate", a.withSession(a.handleInviteRotate))
	a.mux.HandleFunc("DELETE /api/v1/houses/{houseId}/members/{memberId}", a.withSession(a.handleRemoveMember))
	a.mux.HandleFunc("PUT /api/v1/houses/{houseId}/rooms/{roomId}/game", a.withSession(a.handleSetGame))
	a.mux.HandleFunc("DELETE /api/v1/houses/{houseId}/rooms/{roomId}/game", a.withSession(a.handleClearGame))
	a.mux.HandleFunc("POST /api/v1/houses/{houseId}/rooms/{roomId}/coordinator-transfer", a.withSession(a.handleCoordinatorTransfer))
	a.mux.HandleFunc("POST /api/v1/houses/{houseId}/rooms/{roomId}/voice-token", a.withSession(a.handleVoiceToken))
	a.mux.HandleFunc("POST /api/v1/livekit/webhook", a.handleLiveKitWebhook)
	a.mux.HandleFunc("GET /api/v1/realtime", a.withSession(a.handleRealtime))
	a.mux.HandleFunc("POST /api/v1/battle/join", a.withSession(a.handleBattleJoin))
	a.mux.HandleFunc("POST /api/v1/battle/{action}", a.withSession(a.handleBattleCommand))
	a.mux.HandleFunc("GET /api/v1/battle/events", a.withSession(a.handleBattleEvents))
	a.mux.HandleFunc("/", a.handleStatic)
}

func (a *App) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := randomID("req")
		w.Header().Set("X-Request-Id", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey("request-id"), requestID)))
	})
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(self), geolocation=(), microphone=(self)")
		connectSources := "'self' ws: wss: https://*.livekit.cloud"
		if endpoint, err := url.Parse(a.cfg.LiveKitURL); err == nil && endpoint.Host != "" && endpoint.User == nil && (endpoint.Scheme == "ws" || endpoint.Scheme == "wss") {
			scheme := "https"
			if endpoint.Scheme == "ws" {
				scheme = "http"
			}
			connectSources += " " + (&url.URL{Scheme: scheme, Host: endpoint.Host}).String()
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; object-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-src https://codenames.game; connect-src "+connectSources+"; media-src 'self' blob:")
		next.ServeHTTP(w, r)
	})
}

func (a *App) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, allowed := range a.cfg.AllowedOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (a *App) withSession(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.originAllowed(r) {
			writeError(w, r, http.StatusForbidden, "origin_rejected", "This request did not come from Roomcade.", nil)
			return
		}
		cookie, err := r.Cookie("roomcade_guest")
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "session_required", "Start a guest session first.", nil)
			return
		}
		ss, err := a.store.sessionByToken(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, "session_expired", "This browser session has expired.", nil)
			return
		}
		if r.URL.Path != "/api/v1/realtime" && r.URL.Path != "/api/v1/battle/events" {
			a.commands.Lock()
			defer a.commands.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "roomcade_guest", Value: cookie.Value, Path: "/", MaxAge: 180 * 24 * 60 * 60, Expires: ss.ExpiresAt, HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
		next(w, r, ss)
	}
}

func decodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 32*1024))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(dataEnvelope{Data: data})
}

func writeRawData(w http.ResponseWriter, status int, raw json.RawMessage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(append([]byte(`{"data":`), raw...), '}'))
}

func idempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		return "", errors.New("an Idempotency-Key between 8 and 128 characters is required")
	}
	return key, nil
}

func apiErrorFor(err error, requestID string) APIError {
	result := APIError{Code: "invalid_request", Message: err.Error(), RequestID: requestID}
	switch {
	case errors.Is(err, errForbidden):
		result.Code, result.Message = "forbidden", "You do not have permission to do that."
	case errors.Is(err, errNotFound), errors.Is(err, sql.ErrNoRows):
		result.Code, result.Message = "not_found", "That Roomcade item could not be found."
	case errors.Is(err, errConflict):
		result.Code, result.Message = "conflict", "Roomcade changed before this action finished. Refresh and try again."
	}
	return result
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, details any) {
	requestID, _ := r.Context().Value(contextKey("request-id")).(string)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: APIError{Code: code, Message: message, RequestID: requestID, Details: details}})
}

func handleStoreError(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := apiErrorFor(err, r.Context().Value(contextKey("request-id")).(string))
	status := http.StatusBadRequest
	if apiErr.Code == "forbidden" {
		status = http.StatusForbidden
	} else if apiErr.Code == "not_found" {
		status = http.StatusNotFound
	} else if apiErr.Code == "conflict" {
		status = http.StatusConflict
	}
	writeError(w, r, status, apiErr.Code, apiErr.Message, apiErr.Details)
}

func (a *App) handleGuestSession(w http.ResponseWriter, r *http.Request) {
	if !a.originAllowed(r) {
		writeError(w, r, http.StatusForbidden, "origin_rejected", "This request did not come from Roomcade.", nil)
		return
	}
	if cookie, err := r.Cookie("roomcade_guest"); err == nil {
		if ss, err := a.store.sessionByToken(r.Context(), cookie.Value); err == nil {
			writeData(w, http.StatusOK, map[string]any{"sessionId": ss.ID, "expiresAt": ss.ExpiresAt.Unix()})
			return
		}
	}
	ss, token, err := a.store.createSession(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "session_failed", "Roomcade could not start a browser session.", nil)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "roomcade_guest", Value: token, Path: "/", MaxAge: 180 * 24 * 60 * 60, Expires: ss.ExpiresAt, HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	writeData(w, http.StatusCreated, map[string]any{"sessionId": ss.ID, "expiresAt": ss.ExpiresAt.Unix()})
}

func (a *App) handleBootstrap(w http.ResponseWriter, r *http.Request, ss session) {
	houses, err := a.store.houseSummaries(r.Context(), ss.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"sessionId": ss.ID, "houses": houses, "embeddingEnabled": a.cfg.EmbeddingEnabled})
}

func (a *App) handleCreateHouse(w http.ResponseWriter, r *http.Request, ss session) {
	key, err := idempotencyKey(r)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if raw, ok := a.store.idempotentResult(r.Context(), ss.ID, key); ok {
		writeRawData(w, http.StatusOK, raw)
		return
	}
	var body struct{ Name, DisplayName string }
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	snapshot, invite, err := a.store.createHouse(r.Context(), ss.ID, body.Name, body.DisplayName)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	result := map[string]any{"house": snapshot, "inviteToken": invite}
	// Preserve idempotent House creation without retaining the raw invite token.
	// A replay returns the same House and the host can generate a fresh invite.
	if err := a.store.saveIdempotentResult(r.Context(), ss.ID, key, map[string]any{"house": snapshot, "inviteToken": ""}); err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, result)
}

func (a *App) handleGetHouse(w http.ResponseWriter, r *http.Request, ss session) {
	houseID := r.PathValue("houseId")
	hub := a.hubs.get(houseID)
	present, version, _ := hub.presence()
	snapshot, err := a.store.snapshot(r.Context(), houseID, ss.ID, present, version)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.store.memberActivity(r.Context(), houseID)
	writeData(w, http.StatusOK, snapshot)
}

func (a *App) handleUpdateHouse(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct{ Name string }
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := a.store.updateHouse(r.Context(), r.PathValue("houseId"), ss.ID, body.Name); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) handleDeleteHouse(w http.ResponseWriter, r *http.Request, ss session) {
	houseID := r.PathValue("houseId")
	if err := a.store.deleteHouse(r.Context(), houseID, ss.ID); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.hubs.get(houseID).closeDeletedHouse()
	writeData(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *App) handleHostTransfer(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct {
		MemberID string `json:"memberId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := a.store.transferHost(r.Context(), r.PathValue("houseId"), ss.ID, body.MemberID); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) handleCreateRoom(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct{ Name, Kind string }
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := a.store.createRoom(r.Context(), r.PathValue("houseId"), ss.ID, body.Name, body.Kind); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) handleUpdateRoom(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct{ Name, Kind string }
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := a.store.updateRoom(r.Context(), r.PathValue("houseId"), r.PathValue("roomId"), ss.ID, body.Name, body.Kind); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) handleDeleteRoom(w http.ResponseWriter, r *http.Request, ss session) {
	houseID, roomID := r.PathValue("houseId"), r.PathValue("roomId")
	occupied := false
	present, _, _ := a.hubs.get(houseID).presence()
	if len(present) > 0 {
		var count int
		_ = a.store.db.QueryRowContext(r.Context(), `SELECT count(*) FROM house_memberships WHERE house_id=? AND last_room_id=? AND id IN (`+placeholders(len(present))+`)`, append([]any{houseID, roomID}, mapKeysAny(present)...)...).Scan(&count)
		occupied = count > 0
	}
	if err := a.store.deleteRoom(r.Context(), houseID, roomID, ss.ID, occupied); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func placeholders(n int) string {
	if n == 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func mapKeysAny(m map[string]bool) []any {
	out := make([]any, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

func (a *App) handleInvitePreview(w http.ResponseWriter, r *http.Request) {
	preview, err := a.store.invitePreview(r.Context(), r.PathValue("token"))
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	delete(preview, "generation")
	writeData(w, http.StatusOK, preview)
}

func (a *App) handleJoinRequest(w http.ResponseWriter, r *http.Request, ss session) {
	key, err := idempotencyKey(r)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if raw, ok := a.store.idempotentResult(r.Context(), ss.ID, key); ok {
		writeRawData(w, http.StatusOK, raw)
		return
	}
	var body struct{ DisplayName string }
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	req, err := a.store.requestJoin(r.Context(), r.PathValue("token"), ss.ID, body.DisplayName)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	if err := a.store.saveIdempotentResult(r.Context(), ss.ID, key, req); err != nil {
		handleStoreError(w, r, err)
		return
	}
	if preview, err := a.store.invitePreview(r.Context(), r.PathValue("token")); err == nil {
		a.hubs.get(preview["houseId"].(string)).broadcast()
	}
	writeData(w, http.StatusCreated, req)
}

func (a *App) handleJoinStatus(w http.ResponseWriter, r *http.Request, ss session) {
	status, err := a.store.joinStatus(r.Context(), r.PathValue("requestId"), ss.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, status)
}

func (a *App) handleJoinDecision(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct{ Decision string }
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	houseID := r.PathValue("houseId")
	if err := a.store.decideJoin(r.Context(), houseID, r.PathValue("requestId"), ss.ID, body.Decision); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.hubs.get(houseID).broadcast()
	writeData(w, http.StatusOK, map[string]string{"status": body.Decision + "d"})
}

func (a *App) handleInviteRotate(w http.ResponseWriter, r *http.Request, ss session) {
	token, err := a.store.rotateInvite(r.Context(), r.PathValue("houseId"), ss.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.hubs.get(r.PathValue("houseId")).broadcast()
	writeData(w, http.StatusOK, map[string]string{"inviteToken": token})
}

func (a *App) handleRemoveMember(w http.ResponseWriter, r *http.Request, ss session) {
	houseID := r.PathValue("houseId")
	host, authErr := a.store.membership(r.Context(), houseID, ss.ID)
	var hostID string
	_ = a.store.db.QueryRowContext(r.Context(), `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&hostID)
	if authErr != nil || host.ID != hostID || host.ID == r.PathValue("memberId") {
		handleStoreError(w, r, errForbidden)
		return
	}
	var targetSession string
	_ = a.store.db.QueryRowContext(r.Context(), `SELECT session_id FROM house_memberships WHERE id=? AND house_id=?`, r.PathValue("memberId"), houseID).Scan(&targetSession)
	target, targetErr := a.store.membership(r.Context(), houseID, targetSession)
	if targetErr != nil {
		handleStoreError(w, r, targetErr)
		return
	}
	if err := a.revokeVoice(r.Context(), target); err != nil {
		writeError(w, r, 503, "voice_unavailable", "Could not disconnect voice. Try removing this member again.", nil)
		return
	}
	if err := a.store.removeMember(r.Context(), houseID, ss.ID, r.PathValue("memberId")); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.hubs.get(houseID).evictMember(r.PathValue("memberId"))
	a.hubs.get(houseID).broadcast()
	writeData(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (a *App) handleSetGame(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct {
		URL              string `json:"url"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	houseID := r.PathValue("houseId")
	if err := a.store.setGame(r.Context(), houseID, r.PathValue("roomId"), ss.ID, body.URL, body.ExpectedRevision); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) handleClearGame(w http.ResponseWriter, r *http.Request, ss session) {
	revision, err := strconv.ParseInt(r.URL.Query().Get("expectedRevision"), 10, 64)
	if err != nil {
		handleStoreError(w, r, errors.New("expectedRevision is required"))
		return
	}
	houseID := r.PathValue("houseId")
	if err := a.store.clearGame(r.Context(), houseID, r.PathValue("roomId"), ss.ID, revision); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) handleCoordinatorTransfer(w http.ResponseWriter, r *http.Request, ss session) {
	var body struct {
		MemberID         string `json:"memberId"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if err := decodeJSON(r, &body); err != nil {
		handleStoreError(w, r, err)
		return
	}
	houseID := r.PathValue("houseId")
	if err := a.store.transferCoordinator(r.Context(), houseID, r.PathValue("roomId"), ss.ID, body.MemberID, body.ExpectedRevision); err != nil {
		handleStoreError(w, r, err)
		return
	}
	a.respondSnapshot(w, r, ss)
}

func (a *App) respondSnapshot(w http.ResponseWriter, r *http.Request, ss session) {
	houseID := r.PathValue("houseId")
	hub := a.hubs.get(houseID)
	present, version, _ := hub.presence()
	snapshot, err := a.store.snapshot(r.Context(), houseID, ss.ID, present, version)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	hub.broadcast()
	writeData(w, http.StatusOK, snapshot)
}

func (a *App) handleVoiceToken(w http.ResponseWriter, r *http.Request, ss session) {
	if a.cfg.LiveKitURL == "" || a.cfg.LiveKitAPIKey == "" || a.cfg.LiveKitSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "voice_unconfigured", "Voice is not configured on this Roomcade server yet.", nil)
		return
	}
	houseID, roomID := r.PathValue("houseId"), r.PathValue("roomId")
	m, err := a.store.membership(r.Context(), houseID, ss.ID)
	present, _, _ := a.hubs.get(houseID).presence()
	if err != nil || m.LastRoomID != roomID || !present[m.ID] {
		writeError(w, r, http.StatusForbidden, "wrong_room", "Join this room before connecting voice.", nil)
		return
	}
	canPublish, canSubscribe := true, true
	grant := &auth.VideoGrant{RoomJoin: true, Room: "roomcade_" + roomID, CanPublish: &canPublish, CanSubscribe: &canSubscribe, CanPublishSources: []string{"microphone", "camera"}}
	token, err := auth.NewAccessToken(a.cfg.LiveKitAPIKey, a.cfg.LiveKitSecret).SetIdentity(fmt.Sprintf("%s_%d", m.ID, m.MediaGeneration)).SetName(m.DisplayName).SetValidFor(2 * time.Minute).AddGrant(grant).ToJWT()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "voice_token_failed", "Voice could not start right now.", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"url": a.cfg.LiveKitURL, "token": token, "mediaGeneration": m.MediaGeneration})
}

func (a *App) handleLiveKitWebhook(w http.ResponseWriter, r *http.Request) {
	if a.cfg.LiveKitAPIKey == "" || a.cfg.LiveKitSecret == "" {
		writeError(w, r, http.StatusServiceUnavailable, "voice_unconfigured", "Voice webhooks are not configured.", nil)
		return
	}
	event, err := webhook.ReceiveWebhookEvent(r, auth.NewSimpleKeyProvider(a.cfg.LiveKitAPIKey, a.cfg.LiveKitSecret))
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "invalid_webhook", "LiveKit webhook verification failed.", nil)
		return
	}
	slog.Info("livekit webhook", "event", event.Event, "id", event.Id)
	writeData(w, http.StatusOK, map[string]string{"status": "received"})
}

func (a *App) handleRealtime(w http.ResponseWriter, r *http.Request, ss session) {
	houseID := r.URL.Query().Get("houseId")
	m, err := a.store.membership(r.Context(), houseID, ss.ID)
	if err != nil {
		handleStoreError(w, r, err)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"roomcade.v1"}, OriginPatterns: a.cfg.AllowedOrigins})
	if err != nil {
		return
	}
	conn.SetReadLimit(16 * 1024)
	defer conn.CloseNow()
	a.store.memberActivity(r.Context(), houseID)
	a.hubs.get(houseID).serve(r.Context(), &realtimeClient{sessionID: ss.ID, memberID: m.ID, conn: conn})
}

func (a *App) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.db.PingContext(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "database_unready", "Database is unavailable.", nil)
		return
	}
	if info, err := os.Stat(filepath.Join(a.cfg.StaticDir, "index.html")); err != nil || !info.Mode().IsRegular() {
		writeError(w, r, http.StatusServiceUnavailable, "frontend_unready", "Frontend build is unavailable.", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if a.cfg.MetricsToken == "" || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")), []byte(a.cfg.MetricsToken)) != 1 {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Metrics credentials are required.", nil)
		return
	}
	var houses, members int
	_ = a.store.db.QueryRowContext(r.Context(), `SELECT count(*) FROM houses`).Scan(&houses)
	_ = a.store.db.QueryRowContext(r.Context(), `SELECT count(*) FROM house_memberships`).Scan(&members)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "roomcade_houses %d\nroomcade_memberships %d\n", houses, members)
}

func (a *App) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, r, http.StatusNotFound, "not_found", "API route not found.", nil)
		return
	}
	path := filepath.Join(a.cfg.StaticDir, filepath.Clean(r.URL.Path))
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		if kind := mime.TypeByExtension(filepath.Ext(path)); kind != "" {
			w.Header().Set("Content-Type", kind)
		}
		http.ServeFile(w, r, path)
		return
	}
	index := filepath.Join(a.cfg.StaticDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		writeError(w, r, http.StatusNotFound, "frontend_missing", "Build the Roomcade frontend first.", nil)
		return
	}
	http.ServeFile(w, r, index)
}

func (a *App) StartCleanup(ctx context.Context) {
	run := func() {
		deleted, err := a.store.cleanup(ctx, a.hubs.connectedHouses())
		if err != nil {
			slog.Error("house cleanup failed", "error", err)
			return
		}
		if deleted > 0 {
			slog.Info("expired houses deleted", "count", deleted)
		}
	}
	run()
	ticker := time.NewTicker(6 * time.Hour)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
