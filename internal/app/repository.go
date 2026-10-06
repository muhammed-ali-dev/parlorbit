package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type membership struct {
	ID              string
	HouseID         string
	DisplayName     string
	LastRoomID      string
	MediaGeneration int64
	JoinedAt        int64
}

func (s *Store) membership(ctx context.Context, houseID, sessionID string) (membership, error) {
	var m membership
	err := s.db.QueryRowContext(ctx, `SELECT id,house_id,display_name,COALESCE(last_room_id,''),media_generation,joined_at FROM house_memberships WHERE house_id=? AND session_id=?`, houseID, sessionID).Scan(&m.ID, &m.HouseID, &m.DisplayName, &m.LastRoomID, &m.MediaGeneration, &m.JoinedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return m, errForbidden
	}
	return m, err
}

func (s *Store) createHouse(ctx context.Context, sessionID, houseName, displayName string) (HouseSnapshot, string, error) {
	name, err := cleanName(houseName, 36)
	if err != nil {
		return HouseSnapshot{}, "", err
	}
	display, err := cleanName(displayName, 24)
	if err != nil {
		return HouseSnapshot{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HouseSnapshot{}, "", err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	houseID, roomID, memberID := randomID("hou"), randomID("rom"), randomID("mem")
	if _, err = tx.ExecContext(ctx, `INSERT INTO houses(id,name,host_member_id,revision,last_activity_at,created_at) VALUES(?,?,?,1,?,?)`, houseID, name, memberID, now, now); err != nil {
		return HouseSnapshot{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO rooms(id,house_id,name,kind,position,created_at) VALUES(?,?,?,'lounge',0,?)`, roomID, houseID, "Living Room", now); err != nil {
		return HouseSnapshot{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO house_memberships(id,house_id,session_id,display_name,last_room_id,joined_at) VALUES(?,?,?,?,?,?)`, memberID, houseID, sessionID, display, roomID, now); err != nil {
		return HouseSnapshot{}, "", err
	}
	invite, err := randomToken(24)
	if err != nil {
		return HouseSnapshot{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO house_invites(house_id,token_hash,token_hint,generation,created_at) VALUES(?,?,?,1,?)`, houseID, hashToken(invite), invite[:6], now); err != nil {
		return HouseSnapshot{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return HouseSnapshot{}, "", err
	}
	snapshot, err := s.snapshot(ctx, houseID, sessionID, nil, 1)
	return snapshot, invite, err
}

func (s *Store) snapshot(ctx context.Context, houseID, sessionID string, connected map[string]bool, presenceVersion int64) (HouseSnapshot, error) {
	self, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return HouseSnapshot{}, err
	}
	var out HouseSnapshot
	var lastActivity int64
	if err = s.db.QueryRowContext(ctx, `SELECT id,name,host_member_id,revision,last_activity_at FROM houses WHERE id=?`, houseID).Scan(&out.ID, &out.Name, &out.HostMemberID, &out.Revision, &lastActivity); errors.Is(err, sql.ErrNoRows) {
		return out, errNotFound
	} else if err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix(lastActivity, 0).Add(30 * 24 * time.Hour).Unix()
	out.PresenceVersion = presenceVersion
	out.SelfMemberID = self.ID
	out.MediaGeneration = self.MediaGeneration
	out.Permissions.IsHost = out.HostMemberID == self.ID
	out.Permissions.CanManageHouse = out.Permissions.IsHost

	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.name,r.kind,r.position,g.provider,g.canonical_url,g.coordinator_member_id,g.revision FROM rooms r LEFT JOIN room_games g ON g.room_id=r.id WHERE r.house_id=? ORDER BY r.position`, houseID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var room Room
		var provider, gameURL, coordinator sql.NullString
		var gameRevision sql.NullInt64
		if err = rows.Scan(&room.ID, &room.Name, &room.Kind, &room.Position, &provider, &gameURL, &coordinator, &gameRevision); err != nil {
			rows.Close()
			return out, err
		}
		if provider.Valid {
			room.Game = &Game{Provider: provider.String, URL: gameURL.String, CoordinatorMemberID: coordinator.String, Revision: gameRevision.Int64}
		}
		out.Rooms = append(out.Rooms, room)
	}
	rows.Close()

	members, err := s.db.QueryContext(ctx, `SELECT id,display_name,COALESCE(last_room_id,''),joined_at FROM house_memberships WHERE house_id=? ORDER BY joined_at,id`, houseID)
	if err != nil {
		return out, err
	}
	for members.Next() {
		var m Member
		if err = members.Scan(&m.ID, &m.DisplayName, &m.ActiveRoomID, &m.JoinedAt); err != nil {
			members.Close()
			return out, err
		}
		if m.ID == out.HostMemberID {
			m.Role = "host"
		} else {
			m.Role = "member"
		}
		m.Connected = connected != nil && connected[m.ID]
		out.Members = append(out.Members, m)
	}
	members.Close()

	if out.Permissions.IsHost {
		pending, err := s.db.QueryContext(ctx, `SELECT id,display_name,status,expires_at FROM join_requests WHERE house_id=? AND status='pending' AND expires_at>? ORDER BY created_at`, houseID, time.Now().Unix())
		if err != nil {
			return out, err
		}
		for pending.Next() {
			var req JoinRequest
			if err = pending.Scan(&req.ID, &req.DisplayName, &req.Status, &req.ExpiresAt); err != nil {
				pending.Close()
				return out, err
			}
			out.PendingRequests = append(out.PendingRequests, req)
		}
		pending.Close()
	}
	return out, nil
}

func touchHouse(ctx context.Context, tx *sql.Tx, houseID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE houses SET revision=revision+1,last_activity_at=? WHERE id=?`, time.Now().Unix(), houseID)
	return err
}

func (s *Store) createRoom(ctx context.Context, houseID, sessionID, roomName, kind string) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	var host string
	_ = s.db.QueryRowContext(ctx, `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&host)
	if host != m.ID {
		return errForbidden
	}
	name, err := cleanName(roomName, 28)
	if err != nil {
		return err
	}
	if !validRoomKind(kind) {
		return fmt.Errorf("invalid room kind")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE house_id=?`, houseID).Scan(&count); err != nil {
		return err
	}
	if count >= maxHouseRooms {
		return errConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO rooms(id,house_id,name,kind,position,created_at) VALUES(?,?,?,?,?,?)`, randomID("rom"), houseID, name, kind, nextRoomPosition(ctx, tx, houseID), time.Now().Unix()); err != nil {
		return err
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func validRoomKind(kind string) bool {
	switch kind {
	case "lounge", "kitchen", "sunroom", "loft":
		return true
	default:
		return false
	}
}

func (s *Store) updateRoom(ctx context.Context, houseID, roomID, sessionID, roomName, kind string) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	var host string
	_ = s.db.QueryRowContext(ctx, `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&host)
	if host != m.ID {
		return errForbidden
	}
	name, err := cleanName(roomName, 28)
	if err != nil || !validRoomKind(kind) {
		return fmt.Errorf("invalid room")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE rooms SET name=?,kind=? WHERE id=? AND house_id=?`, name, kind, roomID, houseID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) deleteRoom(ctx context.Context, houseID, roomID, sessionID string, occupied bool) error {
	if occupied {
		return errConflict
	}
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	var host string
	_ = s.db.QueryRowContext(ctx, `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&host)
	if host != m.ID {
		return errForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE house_id=?`, houseID).Scan(&count); err != nil {
		return err
	}
	if count <= 1 {
		return errConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM rooms WHERE id=? AND house_id=?`, roomID, houseID); err != nil {
		return err
	}
	var first string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM rooms WHERE house_id=? ORDER BY position LIMIT 1`, houseID).Scan(&first); err != nil {
		return err
	}
	_, _ = tx.ExecContext(ctx, `UPDATE house_memberships SET last_room_id=? WHERE house_id=? AND (last_room_id IS NULL OR last_room_id=?)`, first, houseID, roomID)
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

var codenamesSlug = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func normalizeCodenamesURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme != "https" || u.Hostname() != "codenames.game" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("use a private https://codenames.game/r/... link")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "r" || !codenamesSlug.MatchString(parts[1]) {
		return "", fmt.Errorf("use a private https://codenames.game/r/... link")
	}
	return "https://codenames.game/r/" + parts[1], nil
}

func (s *Store) setGame(ctx context.Context, houseID, roomID, sessionID, rawURL string, expectedRevision int64) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	canonical, err := normalizeCodenamesURL(rawURL)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var host string
	if err = tx.QueryRowContext(ctx, `SELECT h.host_member_id FROM houses h JOIN rooms r ON r.house_id=h.id WHERE h.id=? AND r.id=?`, houseID, roomID).Scan(&host); err != nil {
		return err
	}
	var coordinator sql.NullString
	var revision sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT coordinator_member_id,revision FROM room_games WHERE room_id=?`, roomID).Scan(&coordinator, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (!revision.Valid && expectedRevision != 0) || (revision.Valid && revision.Int64 != expectedRevision) {
		return errConflict
	}
	if coordinator.Valid && coordinator.String != m.ID && host != m.ID {
		return errForbidden
	}
	newRevision := int64(1)
	if revision.Valid {
		newRevision = revision.Int64 + 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO room_games(room_id,provider,canonical_url,coordinator_member_id,revision,updated_at) VALUES(?,'codenames',?,?,?,?) ON CONFLICT(room_id) DO UPDATE SET canonical_url=excluded.canonical_url,coordinator_member_id=excluded.coordinator_member_id,revision=excluded.revision,updated_at=excluded.updated_at`, roomID, canonical, m.ID, newRevision, time.Now().Unix())
	if err != nil {
		return err
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) clearGame(ctx context.Context, houseID, roomID, sessionID string, expectedRevision int64) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var host, coordinator string
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT h.host_member_id,g.coordinator_member_id,g.revision FROM houses h JOIN rooms r ON r.house_id=h.id JOIN room_games g ON g.room_id=r.id WHERE h.id=? AND r.id=?`, houseID, roomID).Scan(&host, &coordinator, &revision); err != nil {
		return errNotFound
	}
	if host != m.ID && coordinator != m.ID {
		return errForbidden
	}
	if revision != expectedRevision {
		return errConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM room_games WHERE room_id=?`, roomID); err != nil {
		return err
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) switchRoom(ctx context.Context, houseID, sessionID, roomID string) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	var exists int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM rooms WHERE id=? AND house_id=?`, roomID, houseID).Scan(&exists); err != nil || exists == 0 {
		return errNotFound
	}
	_, err = s.db.ExecContext(ctx, `UPDATE house_memberships SET last_room_id=?,media_generation=media_generation+1 WHERE id=?`, roomID, m.ID)
	if err == nil {
		_, _ = s.db.ExecContext(ctx, `UPDATE houses SET revision=revision+1,last_activity_at=? WHERE id=?`, time.Now().Unix(), houseID)
	}
	return err
}

func (s *Store) memberActivity(ctx context.Context, houseID string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE houses SET last_activity_at=? WHERE id=?`, time.Now().Unix(), houseID)
}

func (s *Store) invitePreview(ctx context.Context, token string) (map[string]any, error) {
	var houseID, name string
	var generation, members int
	err := s.db.QueryRowContext(ctx, `SELECT h.id,h.name,i.generation,(SELECT count(*) FROM house_memberships m WHERE m.house_id=h.id) FROM house_invites i JOIN houses h ON h.id=i.house_id WHERE i.token_hash=?`, hashToken(token)).Scan(&houseID, &name, &generation, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return map[string]any{"houseId": houseID, "houseName": name, "memberCount": members, "capacity": maxHouseMembers, "full": members >= maxHouseMembers, "generation": generation}, err
}

func (s *Store) requestJoin(ctx context.Context, token, sessionID, displayName string) (JoinRequest, error) {
	preview, err := s.invitePreview(ctx, token)
	if err != nil {
		return JoinRequest{}, err
	}
	if preview["full"].(bool) {
		return JoinRequest{}, errConflict
	}
	name, err := cleanName(displayName, 24)
	if err != nil {
		return JoinRequest{}, err
	}
	houseID := preview["houseId"].(string)
	generation := preview["generation"].(int)
	var exists int
	_ = s.db.QueryRowContext(ctx, `SELECT count(*) FROM house_memberships WHERE house_id=? AND session_id=?`, houseID, sessionID).Scan(&exists)
	if exists > 0 {
		return JoinRequest{}, errConflict
	}
	now := time.Now().Unix()
	req := JoinRequest{ID: randomID("req"), DisplayName: name, Status: "pending", ExpiresAt: time.Unix(now, 0).Add(24 * time.Hour).Unix()}
	err = s.db.QueryRowContext(ctx, `INSERT INTO join_requests(id,house_id,session_id,invite_generation,display_name,status,created_at,expires_at) VALUES(?,?,?,?,?,'pending',?,?) ON CONFLICT(house_id,session_id,status) DO UPDATE SET display_name=excluded.display_name,invite_generation=excluded.invite_generation,created_at=excluded.created_at,expires_at=excluded.expires_at RETURNING id`, req.ID, houseID, sessionID, generation, name, now, req.ExpiresAt).Scan(&req.ID)
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE houses SET revision=revision+1 WHERE id=?`, houseID)
	}
	return req, err
}

func (s *Store) joinStatus(ctx context.Context, requestID, sessionID string) (map[string]any, error) {
	var status, houseID string
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT status,house_id,expires_at FROM join_requests WHERE id=? AND session_id=?`, requestID, sessionID).Scan(&status, &houseID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if status == "pending" && expires < time.Now().Unix() {
		status = "expired"

	}
	result := map[string]any{"id": requestID, "houseId": houseID, "status": status, "expiresAt": expires}
	if status == "approved" {
		var roomID string
		if err = s.db.QueryRowContext(ctx, `SELECT last_room_id FROM house_memberships WHERE house_id=? AND session_id=?`, houseID, sessionID).Scan(&roomID); err != nil {
			return nil, errNotFound
		}
		result["roomId"] = roomID
	}
	return result, err
}

func (s *Store) decideJoin(ctx context.Context, houseID, requestID, sessionID, decision string) error {
	host, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	var hostID string
	_ = s.db.QueryRowContext(ctx, `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&hostID)
	if host.ID != hostID {
		return errForbidden
	}
	if decision != "approve" && decision != "decline" {
		return fmt.Errorf("invalid decision")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var reqSession, display, status string
	var expires int64
	if err = tx.QueryRowContext(ctx, `SELECT session_id,display_name,status,expires_at FROM join_requests WHERE id=? AND house_id=?`, requestID, houseID).Scan(&reqSession, &display, &status, &expires); err != nil {
		return errNotFound
	}
	if status != "pending" || expires < time.Now().Unix() {
		return errConflict
	}
	if decision == "approve" {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM house_memberships WHERE house_id=?`, houseID).Scan(&count); err != nil {
			return err
		}
		if count >= maxHouseMembers {
			return errConflict
		}
		var firstRoom string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM rooms WHERE house_id=? ORDER BY position LIMIT 1`, houseID).Scan(&firstRoom); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO house_memberships(id,house_id,session_id,display_name,last_room_id,joined_at) VALUES(?,?,?,?,?,?)`, randomID("mem"), houseID, reqSession, display, firstRoom, time.Now().Unix()); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return errConflict
			}
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM join_requests WHERE house_id=? AND session_id=? AND status=?`, houseID, reqSession, decision+"d"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE join_requests SET status=? WHERE id=?`, decision+"d", requestID); err != nil {
		return err
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) rotateInvite(ctx context.Context, houseID, sessionID string) (string, error) {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return "", err
	}
	var host string
	_ = s.db.QueryRowContext(ctx, `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&host)
	if host != m.ID {
		return "", errForbidden
	}
	token, err := randomToken(24)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE house_invites SET token_hash=?,token_hint=?,generation=generation+1,created_at=? WHERE house_id=?`, hashToken(token), token[:6], time.Now().Unix(), houseID); err != nil {
		return "", err
	}
	_, _ = tx.ExecContext(ctx, `DELETE FROM join_requests WHERE house_id=? AND status='revoked'`, houseID)
	_, _ = tx.ExecContext(ctx, `UPDATE join_requests SET status='revoked' WHERE house_id=? AND status='pending'`, houseID)
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (s *Store) updateHouse(ctx context.Context, houseID, sessionID, name string) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	clean, err := cleanName(name, 36)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE houses SET name=?,revision=revision+1,last_activity_at=? WHERE id=? AND host_member_id=?`, clean, time.Now().Unix(), houseID, m.ID)
	if n, _ := affected(res); err == nil && n == 0 {
		return errForbidden
	}
	return err
}

func (s *Store) transferHost(ctx context.Context, houseID, sessionID, targetMemberID string) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE houses SET host_member_id=?,revision=revision+1,last_activity_at=? WHERE id=? AND host_member_id=? AND EXISTS(SELECT 1 FROM house_memberships WHERE id=? AND house_id=?)`, targetMemberID, time.Now().Unix(), houseID, m.ID, targetMemberID, houseID)
	if n, _ := affected(res); err == nil && n == 0 {
		return errForbidden
	}
	return err
}

func (s *Store) removeMember(ctx context.Context, houseID, sessionID, targetID string) error {
	host, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	if host.ID == targetID {
		return errConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hostID string
	_ = tx.QueryRowContext(ctx, `SELECT host_member_id FROM houses WHERE id=?`, houseID).Scan(&hostID)
	if hostID != host.ID {
		return errForbidden
	}
	_, _ = tx.ExecContext(ctx, `UPDATE room_games SET coordinator_member_id=?,revision=revision+1 WHERE coordinator_member_id=?`, host.ID, targetID)
	res, err := tx.ExecContext(ctx, `DELETE FROM house_memberships WHERE id=? AND house_id=?`, targetID, houseID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) deleteHouse(ctx context.Context, houseID, sessionID string) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM houses WHERE id=? AND host_member_id=?`, houseID, m.ID)
	if n, _ := affected(res); err == nil && n == 0 {
		return errForbidden
	}
	return err
}

func (s *Store) transferCoordinator(ctx context.Context, houseID, roomID, sessionID, targetID string, expectedRevision int64) error {
	m, err := s.membership(ctx, houseID, sessionID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var host, coordinator string
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT h.host_member_id,g.coordinator_member_id,g.revision FROM houses h JOIN rooms r ON r.house_id=h.id JOIN room_games g ON g.room_id=r.id WHERE h.id=? AND r.id=?`, houseID, roomID).Scan(&host, &coordinator, &revision)
	if err != nil {
		return errNotFound
	}
	if m.ID != host && m.ID != coordinator {
		return errForbidden
	}
	if revision != expectedRevision {
		return errConflict
	}
	var targetExists int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM house_memberships WHERE id=? AND house_id=?`, targetID, houseID).Scan(&targetExists); err != nil || targetExists == 0 {
		return errNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE room_games SET coordinator_member_id=?,revision=revision+1,updated_at=? WHERE room_id=?`, targetID, time.Now().Unix(), roomID); err != nil {
		return err
	}
	if err = touchHouse(ctx, tx, houseID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) cleanup(ctx context.Context, connectedHouses map[string]bool) (int64, error) {
	cutoff := time.Now().Add(-30 * 24 * time.Hour).Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM houses WHERE last_activity_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		if !connectedHouses[id] {
			ids = append(ids, id)
		}
	}
	rows.Close()
	sort.Strings(ids)
	var deleted int64
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, `DELETE FROM houses WHERE id=? AND last_activity_at<?`, id, cutoff)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

func affected(result sql.Result) (int64, error) {
	if result == nil {
		return 0, nil
	}
	return result.RowsAffected()
}

func nextRoomPosition(ctx context.Context, tx *sql.Tx, houseID string) int {
	var position int
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position),-1)+1 FROM rooms WHERE house_id=?`, houseID).Scan(&position)
	return position
}
