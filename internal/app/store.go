package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS battle_ai_usage (bucket TEXT PRIMARY KEY, attempts INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS demo_login_attempts (bucket TEXT PRIMARY KEY, attempts INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS guest_sessions (
  id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS houses (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, host_member_id TEXT,
  revision INTEGER NOT NULL DEFAULT 1, last_activity_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS rooms (
  id TEXT PRIMARY KEY, house_id TEXT NOT NULL REFERENCES houses(id) ON DELETE CASCADE,
  name TEXT NOT NULL, kind TEXT NOT NULL, position INTEGER NOT NULL, created_at INTEGER NOT NULL,
  UNIQUE(house_id, position)
);
CREATE TABLE IF NOT EXISTS house_memberships (
  id TEXT PRIMARY KEY, house_id TEXT NOT NULL REFERENCES houses(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL REFERENCES guest_sessions(id) ON DELETE CASCADE,
  display_name TEXT NOT NULL COLLATE NOCASE, last_room_id TEXT REFERENCES rooms(id) ON DELETE SET NULL,
  media_generation INTEGER NOT NULL DEFAULT 1, joined_at INTEGER NOT NULL,
  UNIQUE(house_id, session_id), UNIQUE(house_id, display_name)
);
CREATE TABLE IF NOT EXISTS room_games (
  room_id TEXT PRIMARY KEY REFERENCES rooms(id) ON DELETE CASCADE,
  provider TEXT NOT NULL, canonical_url TEXT NOT NULL,
  coordinator_member_id TEXT NOT NULL REFERENCES house_memberships(id) ON DELETE RESTRICT,
  revision INTEGER NOT NULL DEFAULT 1, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS house_invites (
  house_id TEXT PRIMARY KEY REFERENCES houses(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE, token_hint TEXT NOT NULL, generation INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS join_requests (
  id TEXT PRIMARY KEY, house_id TEXT NOT NULL REFERENCES houses(id) ON DELETE CASCADE,
  session_id TEXT NOT NULL REFERENCES guest_sessions(id) ON DELETE CASCADE,
  invite_generation INTEGER NOT NULL, display_name TEXT NOT NULL COLLATE NOCASE,
  status TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL,
  UNIQUE(house_id, session_id, status)
);
CREATE TABLE IF NOT EXISTS idempotency_keys (
  session_id TEXT NOT NULL, key TEXT NOT NULL, response_json TEXT NOT NULL,
  created_at INTEGER NOT NULL, PRIMARY KEY(session_id, key)
);
CREATE INDEX IF NOT EXISTS idx_members_house ON house_memberships(house_id);
CREATE INDEX IF NOT EXISTS idx_rooms_house ON rooms(house_id, position);
CREATE INDEX IF NOT EXISTS idx_requests_house ON join_requests(house_id, status);
INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES (1, unixepoch());
`

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return s.db.Close()
}

func randomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Store) createSession(ctx context.Context) (session, string, error) {
	token, err := randomToken(32)
	if err != nil {
		return session{}, "", err
	}
	now := time.Now().UTC()
	ss := session{ID: randomID("ses"), TokenHash: hashToken(token), ExpiresAt: now.Add(180 * 24 * time.Hour)}
	_, err = s.db.ExecContext(ctx, `INSERT INTO guest_sessions(id,token_hash,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?)`, ss.ID, ss.TokenHash, now.Unix(), now.Unix(), ss.ExpiresAt.Unix())
	return ss, token, err
}

func (s *Store) sessionByToken(ctx context.Context, token string) (session, error) {
	var ss session
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT id,token_hash,expires_at FROM guest_sessions WHERE token_hash=? AND expires_at>?`, hashToken(token), time.Now().Unix()).Scan(&ss.ID, &ss.TokenHash, &expires)
	if err != nil {
		return ss, err
	}
	ss.ExpiresAt = time.Unix(expires, 0)
	newExpiry := time.Now().UTC().Add(180 * 24 * time.Hour)
	_, _ = s.db.ExecContext(ctx, `UPDATE guest_sessions SET last_seen_at=?,expires_at=? WHERE id=?`, time.Now().Unix(), newExpiry.Unix(), ss.ID)
	ss.ExpiresAt = newExpiry
	return ss, nil
}

func (s *Store) houseSummaries(ctx context.Context, sessionID string) ([]HouseSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.id,h.name,(SELECT count(*) FROM rooms r WHERE r.house_id=h.id),(SELECT count(*) FROM house_memberships x WHERE x.house_id=h.id),COALESCE(m.last_room_id,'') FROM houses h JOIN house_memberships m ON m.house_id=h.id WHERE m.session_id=? ORDER BY h.last_activity_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HouseSummary{}
	for rows.Next() {
		var h HouseSummary
		if err := rows.Scan(&h.ID, &h.Name, &h.RoomCount, &h.MemberCount, &h.LastRoomID); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) idempotentResult(ctx context.Context, sessionID, key string) (json.RawMessage, bool) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT response_json FROM idempotency_keys WHERE session_id=? AND key=? AND created_at>?`, sessionID, key, time.Now().Add(-24*time.Hour).Unix()).Scan(&raw)
	return json.RawMessage(raw), err == nil
}

func (s *Store) saveIdempotentResult(ctx context.Context, sessionID, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO idempotency_keys(session_id,key,response_json,created_at) VALUES(?,?,?,?)`, sessionID, key, string(raw), time.Now().Unix())
	return err
}

func cleanName(value string, max int) (string, error) {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) < 2 || len([]rune(value)) > max {
		return "", fmt.Errorf("must be between 2 and %d characters", max)
	}
	return value, nil
}

var errForbidden = errors.New("forbidden")
var errNotFound = errors.New("not found")
var errConflict = errors.New("conflict")
