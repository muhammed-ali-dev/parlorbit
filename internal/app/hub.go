package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type realtimeClient struct {
	sessionID string
	memberID  string
	conn      *websocket.Conn
	outbox    *realtimeOutbox
}

func (c *realtimeClient) write(ctx context.Context, value any) error {
	return c.outbox.enqueue(ctx, realtimePacket{value: value})
}

func (c *realtimeClient) writeAndClose(value any, status websocket.StatusCode, reason string) {
	_ = c.outbox.enqueue(context.Background(), realtimePacket{value: value, after: func() { _ = c.conn.Close(status, reason) }})
}

type houseHub struct {
	houseID           string
	store             *Store
	mu                sync.Mutex
	clients           map[string]*realtimeClient
	presenceVersion   int64
	broadcastMu       sync.Mutex
	commands          *sync.Mutex
	revokeVoice       func(context.Context, membership) error
	coordinatorTimers map[string]*time.Timer
	hostTimer         *time.Timer
	hostTimerFor      string
}

type hubManager struct {
	commands    *sync.Mutex
	revokeVoice func(context.Context, membership) error
	store       *Store
	mu          sync.Mutex
	hubs        map[string]*houseHub
}

func newHubManager(store *Store) *hubManager {
	return &hubManager{store: store, hubs: make(map[string]*houseHub)}
}

func (m *hubManager) get(houseID string) *houseHub {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.hubs[houseID]
	if h == nil {
		h = &houseHub{houseID: houseID, store: m.store, clients: make(map[string]*realtimeClient), presenceVersion: time.Now().UnixMilli(), commands: m.commands, revokeVoice: m.revokeVoice, coordinatorTimers: make(map[string]*time.Timer)}
		m.hubs[houseID] = h
	}
	return h
}

func (m *hubManager) connectedHouses() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool)
	for id, hub := range m.hubs {
		hub.mu.Lock()
		out[id] = len(hub.clients) > 0
		hub.mu.Unlock()
	}
	return out
}

func (h *houseHub) attach(client *realtimeClient) {
	h.mu.Lock()
	previous := h.clients[client.sessionID]
	h.clients[client.sessionID] = client
	if timer := h.coordinatorTimers[client.memberID]; timer != nil {
		timer.Stop()
		delete(h.coordinatorTimers, client.memberID)
	}
	h.presenceVersion++
	if h.hostTimer != nil && h.hostTimerFor == client.memberID {
		h.hostTimer.Stop()
		h.hostTimer = nil
		h.hostTimerFor = ""
	}
	h.mu.Unlock()
	if previous != nil {
		previous.writeAndClose(map[string]any{"type": "session.replaced"}, websocket.StatusPolicyViolation, "session replaced")
	}
	h.broadcast()
	var host string
	_ = h.store.db.QueryRow(`SELECT host_member_id FROM houses WHERE id=?`, h.houseID).Scan(&host)
	present, _, _ := h.presence()
	if host != "" && !present[host] {
		h.scheduleHostRecovery(host)
	}
	h.recoverCoordinators("")
}

func (h *houseHub) detach(client *realtimeClient) {
	h.mu.Lock()
	if h.clients[client.sessionID] != client {
		h.mu.Unlock()
		return
	}
	delete(h.clients, client.sessionID)
	h.presenceVersion++
	h.mu.Unlock()
	h.broadcast()
	h.scheduleHostRecovery(client.memberID)
	h.mu.Lock()
	if timer := h.coordinatorTimers[client.memberID]; timer != nil {
		timer.Stop()
	}
	h.coordinatorTimers[client.memberID] = time.AfterFunc(30*time.Second, func() {
		h.commands.Lock()
		defer h.commands.Unlock()
		present, _, _ := h.presence()
		if !present[client.memberID] {
			h.mu.Lock()
			delete(h.coordinatorTimers, client.memberID)
			h.mu.Unlock()
			h.recoverCoordinators(client.memberID)
			h.broadcast()
		}
	})
	h.mu.Unlock()
}

func (h *houseHub) evictMember(memberID string) {
	h.mu.Lock()
	var targets []*realtimeClient
	for _, client := range h.clients {
		if client.memberID == memberID {
			targets = append(targets, client)
		}
	}
	h.mu.Unlock()
	for _, client := range targets {
		client.writeAndClose(map[string]any{"type": "membership.removed"}, websocket.StatusPolicyViolation, "membership removed")
	}
}

func (h *houseHub) closeDeletedHouse() {
	h.mu.Lock()
	clients := make([]*realtimeClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.Unlock()
	for _, client := range clients {
		client.writeAndClose(map[string]any{"type": "house.deleted"}, websocket.StatusNormalClosure, "house deleted")
	}
}

func (h *houseHub) presence() (map[string]bool, int64, []*realtimeClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	present := make(map[string]bool, len(h.clients))
	clients := make([]*realtimeClient, 0, len(h.clients))
	for _, client := range h.clients {
		present[client.memberID] = true
		clients = append(clients, client)
	}
	return present, h.presenceVersion, clients
}

func (h *houseHub) broadcast() {
	h.broadcastMu.Lock()
	defer h.broadcastMu.Unlock()
	h.mu.Lock()
	h.presenceVersion++
	h.mu.Unlock()
	present, version, clients := h.presence()
	for _, client := range clients {
		snapshot, err := h.store.snapshot(context.Background(), h.houseID, client.sessionID, present, version)
		if err != nil {
			continue
		}
		func(c *realtimeClient, snap HouseSnapshot) {
			_ = c.write(context.Background(), map[string]any{"type": "house.snapshot", "data": snap})
		}(client, snapshot)
	}
}

func (h *houseHub) scheduleHostRecovery(disconnectedMemberID string) {
	var host string
	if err := h.store.db.QueryRow(`SELECT host_member_id FROM houses WHERE id=?`, h.houseID).Scan(&host); err != nil || host != disconnectedMemberID {
		return
	}
	h.mu.Lock()
	if h.hostTimer != nil && h.hostTimerFor == disconnectedMemberID {
		h.mu.Unlock()
		return
	}
	if h.hostTimer != nil {
		h.hostTimer.Stop()
	}
	h.hostTimer = time.AfterFunc(30*time.Second, func() {
		h.commands.Lock()
		defer h.commands.Unlock()
		h.mu.Lock()
		h.hostTimer = nil
		h.hostTimerFor = ""
		h.mu.Unlock()
		present, _, clients := h.presence()
		if present[disconnectedMemberID] || len(clients) == 0 {
			return
		}
		candidate := clients[0].memberID
		var joined int64
		_ = h.store.db.QueryRow(`SELECT joined_at FROM house_memberships WHERE id=?`, candidate).Scan(&joined)
		for _, client := range clients[1:] {
			var other int64
			_ = h.store.db.QueryRow(`SELECT joined_at FROM house_memberships WHERE id=?`, client.memberID).Scan(&other)
			if other < joined || (other == joined && client.memberID < candidate) {
				candidate, joined = client.memberID, other
			}
		}
		_, _ = h.store.db.Exec(`UPDATE houses SET host_member_id=?,revision=revision+1,last_activity_at=? WHERE id=? AND host_member_id=?`, candidate, time.Now().Unix(), h.houseID, disconnectedMemberID)
		h.broadcast()
	})
	h.hostTimerFor = disconnectedMemberID
	h.mu.Unlock()
}

func (h *houseHub) serve(ctx context.Context, client *realtimeClient) {
	client.outbox = newRealtimeOutbox(ctx, func(writeCtx context.Context, value any) error {
		writeCtx, cancel := context.WithTimeout(writeCtx, 5*time.Second)
		defer cancel()
		return wsjson.Write(writeCtx, client.conn, value)
	}, func() { _ = client.conn.CloseNow() })
	defer client.outbox.stop()
	h.commands.Lock()
	h.attach(client)
	h.commands.Unlock()
	defer func() { h.commands.Lock(); defer h.commands.Unlock(); h.detach(client) }()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	readErr := make(chan error, 1)
	go func() {
		for {
			var msg struct {
				Type         string `json:"type"`
				TargetRoomID string `json:"targetRoomId"`
				TransitionID string `json:"transitionId"`
			}
			if err := wsjson.Read(ctx, client.conn, &msg); err != nil {
				readErr <- err
				return
			}
			switch msg.Type {
			case "house.enter", "presence.resync":
				h.commands.Lock()
				h.broadcast()
				h.commands.Unlock()
			case "room.switch":
				h.commands.Lock()
				h.mu.Lock()
				active := h.clients[client.sessionID] == client
				h.mu.Unlock()
				var err error
				if !active {
					err = errForbidden
				} else {
					member, memberErr := h.store.membership(ctx, h.houseID, client.sessionID)
					err = memberErr
					if err == nil && member.LastRoomID != msg.TargetRoomID {
						var exists int
						_ = h.store.db.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE id=? AND house_id=?`, msg.TargetRoomID, h.houseID).Scan(&exists)
						if exists == 0 {
							err = errNotFound
						} else if err = h.revokeVoice(ctx, member); err == nil {
							err = h.store.switchRoom(ctx, h.houseID, client.sessionID, msg.TargetRoomID)
							if err == nil {
								h.recoverCoordinators(member.ID)
							}
						}
					}
				}
				if err != nil {
					_ = client.write(ctx, map[string]any{"type": "command.rejected", "transitionId": msg.TransitionID, "error": apiErrorFor(err, "")})
				} else {
					_ = client.write(ctx, map[string]any{"type": "command.accepted", "transitionId": msg.TransitionID})
					h.broadcast()
				}
				h.commands.Unlock()
			case "connection.pong":
			default:
				_ = client.write(ctx, map[string]any{"type": "command.rejected", "error": APIError{Code: "unknown_command", Message: "Unknown realtime command."}})
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			return
		case <-ping.C:
			if err := client.write(ctx, map[string]any{"type": "connection.ping", "at": time.Now().Unix()}); err != nil {
				return
			}
		}
	}
}

func decodeRealtime(raw []byte, target any) error {
	if len(raw) > 16*1024 {
		return errors.New("message too large")
	}
	return json.Unmarshal(raw, target)
}

// Transfer only when the coordinator has deliberately left or their grace
// period elapsed. An absent coordinator is also recovered when a room refills.
func (h *houseHub) recoverCoordinators(departed string) {
	present, _, clients := h.presence()
	if len(clients) == 0 {
		return
	}
	snapshot, err := h.store.snapshot(context.Background(), h.houseID, clients[0].sessionID, present, 0)
	if err != nil {
		return
	}
	for _, room := range snapshot.Rooms {
		if room.Game == nil {
			continue
		}
		coordinator := room.Game.CoordinatorMemberID
		h.mu.Lock()
		timerPending := h.coordinatorTimers[coordinator] != nil
		h.mu.Unlock()
		if departed == "" && timerPending {
			continue
		}
		if departed != "" && coordinator != departed {
			continue
		}
		var candidate string
		valid := false
		for _, member := range snapshot.Members {
			if member.ID == coordinator && member.Connected && member.ActiveRoomID == room.ID {
				valid = true
			}
			if candidate == "" && member.Connected && member.ActiveRoomID == room.ID {
				candidate = member.ID
			}
		}
		if !valid && candidate != "" {
			_, err = h.store.db.Exec(`UPDATE room_games SET coordinator_member_id=?,revision=revision+1 WHERE room_id=? AND coordinator_member_id=?`, candidate, room.ID, coordinator)
			if err == nil {
				_, _ = h.store.db.Exec(`UPDATE houses SET revision=revision+1 WHERE id=?`, h.houseID)
			}
		}
	}
}
