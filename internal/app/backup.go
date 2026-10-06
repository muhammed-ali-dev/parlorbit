package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

func openExistingDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be a regular file")
	}
	uri := url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// BackupDatabase creates a consistent standalone SQLite snapshot, including committed WAL data.
// The destination must be new. It is never a replacement for an existing database.
func BackupDatabase(ctx context.Context, source, destination string) error {
	db, err := openExistingDatabase(source)
	if err != nil {
		return err
	}
	defer db.Close()
	target, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create new backup: %w", err)
	}
	if err = file.Close(); err != nil {
		os.Remove(target)
		return err
	}
	complete := false
	defer func() {
		if !complete {
			os.Remove(target)
		}
	}()
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if err = VerifyDatabase(ctx, target); err != nil {
		return fmt.Errorf("verify snapshot: %w", err)
	}
	file, err = os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	complete = true
	return nil
}

// VerifyDatabase validates an existing snapshot without creating or migrating it.
func VerifyDatabase(ctx context.Context, path string) error {
	db, err := openExistingDatabase(path)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	for rows.Next() {
		var result string
		if err = rows.Scan(&result); err != nil {
			rows.Close()
			return err
		}
		if result != "ok" {
			rows.Close()
			return fmt.Errorf("integrity check: %s", result)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("foreign key check failed")
	}
	// Reject unrelated SQLite files as well as broken application tables.
	var count int
	for _, table := range []string{"schema_migrations", "guest_sessions", "houses", "rooms", "house_memberships", "room_games", "house_invites", "join_requests", "battle_ai_usage", "demo_login_attempts"} {
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			return fmt.Errorf("application schema: %w", err)
		}
	}
	return nil
}
